package apps

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Pairing limits (plan pipeline step 14).
const (
	// PairingTTL is how long a request waits for the admin.
	PairingTTL = 10 * time.Minute
	// ClaimWindow is how long an approved request stays claimable. It starts
	// at the approval, so an approval in the last seconds of the TTL is not
	// lost to the app's polling interval.
	ClaimWindow = 5 * time.Minute
	// MaxPendingPairings caps the open requests; more are answered 429.
	MaxPendingPairings = 20
	// PairingCreatesPerMinute limits POST /pairing/requests per source IP.
	PairingCreatesPerMinute = 5
	// PairingPollsPerSecond (burst PairingPollBurst) limits polling per IP.
	PairingPollsPerSecond = 2
	PairingPollBurst      = 20

	pairingRetention = time.Hour
)

// CreatePairing opens a pairing request from the app at ip. The poll secret
// is returned here only; the database keeps its SHA-256.
func (s *Service) CreatePairing(ctx context.Context, ip string, in protocol.PairingRequestInput) (protocol.PairingCreated, error) {
	name, err := cleanName(strings.TrimSpace(in.AppName))
	if err != nil {
		return protocol.PairingCreated{}, invalid("app_name", "app_name is required and at most 64 characters")
	}
	desc, err := cleanDescription(in.Description)
	if err != nil {
		return protocol.PairingCreated{}, err
	}
	scopes, err := NormalizeScopes("requested_scopes", in.RequestedScopes)
	if err != nil {
		return protocol.PairingCreated{}, err
	}
	if !validReplacesID(in.ReplacesAppID) {
		return protocol.PairingCreated{}, invalid("replaces_app_id", "replaces_app_id must be an app id: at most 64 letters, digits, '-' or '_'")
	}
	if ok, wait := s.pairCreate.Allow(auth.IPKey(ip)); !ok {
		return protocol.PairingCreated{}, &auth.RateLimitedError{RetryAfter: wait}
	}
	secret, err := auth.NewToken()
	if err != nil {
		return protocol.PairingCreated{}, err
	}
	scopesJSON, err := json.Marshal(scopes)
	if err != nil {
		return protocol.PairingCreated{}, fmt.Errorf("encode scopes: %w", err)
	}
	return run(ctx, s, func(tx *store.Tx) (protocol.PairingCreated, error) {
		now := tx.Now()
		if err := expireStale(tx, now); err != nil {
			return protocol.PairingCreated{}, err
		}
		if _, err := tx.Exec(`DELETE FROM pairing_requests WHERE state != 'pending' AND expires_at < ?`, now-pairingRetention.Milliseconds()); err != nil {
			return protocol.PairingCreated{}, fmt.Errorf("purge pairing requests: %w", err)
		}
		var pending int
		var earliest sql.NullInt64
		if err := tx.QueryRow(`SELECT COUNT(*), MIN(expires_at) FROM pairing_requests WHERE state = 'pending'`).Scan(&pending, &earliest); err != nil {
			return protocol.PairingCreated{}, fmt.Errorf("count pairing requests: %w", err)
		}
		if pending >= MaxPendingPairings {
			wait := time.Second
			if earliest.Valid {
				wait = max(time.Duration(earliest.Int64-now)*time.Millisecond, time.Second)
			}
			return protocol.PairingCreated{}, &auth.RateLimitedError{RetryAfter: wait}
		}
		code, err := freeCode(tx)
		if err != nil {
			return protocol.PairingCreated{}, err
		}
		replaces, err := existingApp(tx, in.ReplacesAppID)
		if err != nil {
			return protocol.PairingCreated{}, err
		}
		id, expires := tx.NewID(), now+PairingTTL.Milliseconds()
		_, err = tx.Exec(`INSERT INTO pairing_requests (id, app_name, description, requested_scopes, code, poll_secret_hash, source_ip,
				created_at, expires_at, replaces_app_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, name, desc, string(scopesJSON), code, auth.HashToken(secret), ip, now, expires, replaces)
		if err != nil {
			return protocol.PairingCreated{}, fmt.Errorf("insert pairing request: %w", err)
		}
		return protocol.PairingCreated{ID: id, PollSecret: secret, Code: code, ExpiresAt: protocol.FormatTime(expires)}, nil
	})
}

// validReplacesID reports whether id is empty (no app named) or can be an
// app id: at most 64 ASCII letters, digits, '-' or '_'.
func validReplacesID(id string) bool {
	if len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// existingApp returns id for the replaces_app_id column when it names an
// app, else nil: the column is a foreign key, so an id that names no app is
// not stored and the request simply names no app.
func existingApp(tx *store.Tx, id string) (any, error) {
	if id == "" {
		return nil, nil
	}
	var one int
	err := tx.QueryRowContext(tx.Context(), `SELECT 1 FROM apps WHERE id = ?`, id).Scan(&one)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("check replaced app: %w", err)
	}
	return id, nil
}

// freeCode draws a six-digit code that no pending request uses.
func freeCode(tx *store.Tx) (string, error) {
	for range 20 {
		n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
		if err != nil {
			return "", fmt.Errorf("generate pairing code: %w", err)
		}
		code := fmt.Sprintf("%06d", n.Int64())
		var one int
		err = tx.QueryRow(`SELECT 1 FROM pairing_requests WHERE code = ? AND state = 'pending'`, code).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return code, nil
		}
		if err != nil {
			return "", fmt.Errorf("check pairing code: %w", err)
		}
	}
	return "", errors.New("no free pairing code")
}

// expireStale turns pending and approved requests past their expiry into
// expired. An approved request the app never claimed also disables that app
// when it was created for the request: it never received a token, so it is
// switched off (not deleted, which keeps the audit trail intact). The app of
// a replaced request is left alone: it existed before, and if it was enabled
// its old tokens still work, because only the claim revokes them.
func expireStale(tx *store.Tx, now int64) error {
	if _, err := tx.Exec(`UPDATE apps SET disabled_at = ? WHERE disabled_at IS NULL AND id IN
		(SELECT approved_app_id FROM pairing_requests
		 WHERE state = 'approved' AND replaced = 0 AND expires_at <= ? AND approved_app_id IS NOT NULL)`, now, now); err != nil {
		return fmt.Errorf("disable unclaimed pairing apps: %w", err)
	}
	if _, err := tx.Exec(`UPDATE pairing_requests SET state = 'expired'
		WHERE state IN ('pending', 'approved') AND expires_at <= ?`, now); err != nil {
		return fmt.Errorf("expire pairing requests: %w", err)
	}
	return nil
}

type pairingRow struct {
	id, appName, description, code, sourceIP, state string
	scopes                                          []string
	secretHash                                      []byte
	approvedApp, replacesApp                        sql.NullString
	replaced                                        bool
	created, expires                                int64
}

// pairingCols never selects token_ciphertext: no token material is stored
// for a pairing request, so there is nothing to read back.
const pairingCols = `id, app_name, description, requested_scopes, code, poll_secret_hash, source_ip, state, approved_app_id,
	replaces_app_id, replaced, created_at, expires_at`

// scanPairing reads one row selected with pairingCols.
func scanPairing(sc scanner) (pairingRow, error) {
	var (
		r      pairingRow
		scopes string
	)
	err := sc.Scan(&r.id, &r.appName, &r.description, &scopes, &r.code, &r.secretHash, &r.sourceIP, &r.state, &r.approvedApp,
		&r.replacesApp, &r.replaced, &r.created, &r.expires)
	if err != nil {
		return r, err
	}
	if r.scopes, err = decodeStrings(scopes); err != nil {
		return r, fmt.Errorf("pairing scopes: %w", err)
	}
	return r, nil
}

func loadPairing(ctx context.Context, q queryer, id string) (pairingRow, error) {
	r, err := scanPairing(q.QueryRowContext(ctx, `SELECT `+pairingCols+` FROM pairing_requests WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, notFound()
	}
	if err != nil {
		return r, fmt.Errorf("load pairing request: %w", err)
	}
	return r, nil
}

// pollerRequest checks the poll secret of request id for the app's own
// calls (poll and cancel), after the per-address poll limit. A missing or
// wrong secret and an unknown id are all ErrNotFound: a wrong secret reveals
// nothing, not even that the id exists.
func (s *Service) pollerRequest(ctx context.Context, ip, id, secret string) (pairingRow, error) {
	if ok, wait := s.pairPoll.Allow(auth.IPKey(ip)); !ok {
		return pairingRow{}, &auth.RateLimitedError{RetryAfter: wait}
	}
	if id == "" || len(id) > 64 || secret == "" || len(secret) > 128 {
		return pairingRow{}, notFound()
	}
	row, err := loadPairing(ctx, s.db.Read(), id)
	if err != nil {
		return pairingRow{}, err
	}
	if subtle.ConstantTimeCompare(row.secretHash, auth.HashToken(secret)) != 1 {
		return pairingRow{}, notFound()
	}
	return row, nil
}

// PollPairing answers the app's poll. The poll secret must match, else the
// request is ErrNotFound (a wrong secret reveals nothing, not even that the
// id exists). No token exists before the first poll after the approval: that
// poll mints it in the same transaction that moves the request to claimed, so
// the token is returned once and never stored in any recoverable form. That
// answer also lists the channels the bot is in, read in the same transaction.
// When the approval replaced an existing app, the same transaction revokes
// every other token of that app, so the app's old copies stop working the
// moment the new one is handed out.
func (s *Service) PollPairing(ctx context.Context, ip, id, secret string) (protocol.PairingStatus, error) {
	row, err := s.pollerRequest(ctx, ip, id, secret)
	if err != nil {
		return protocol.PairingStatus{}, err
	}
	now := s.now().UnixMilli()
	status := protocol.PairingStatus{State: row.state, ExpiresAt: protocol.FormatTime(row.expires)}
	switch {
	case (row.state == protocol.PairingPending || row.state == protocol.PairingApproved) && row.expires <= now:
		// fall through to the write path, which expires it
	case row.state == protocol.PairingApproved:
		// claim below
	default:
		return status, nil
	}
	var revoked []string
	st, err := run(ctx, s, func(tx *store.Tx) (protocol.PairingStatus, error) {
		revoked = nil
		if err := expireStale(tx, tx.Now()); err != nil {
			return status, err
		}
		cur, err := loadPairing(tx.Context(), tx, id)
		if err != nil {
			return status, err
		}
		status = protocol.PairingStatus{State: cur.state, ExpiresAt: protocol.FormatTime(cur.expires)}
		if cur.state != protocol.PairingApproved || !cur.approvedApp.Valid {
			return status, nil
		}
		// An app an admin disabled after the approval gets no token.
		var (
			botID    string
			disabled sql.NullInt64
		)
		err = tx.QueryRowContext(tx.Context(), `SELECT user_id, disabled_at FROM apps WHERE id = ?`, cur.approvedApp.String).Scan(&botID, &disabled)
		if errors.Is(err, sql.ErrNoRows) {
			return status, nil
		}
		if err != nil {
			return status, fmt.Errorf("load pairing app: %w", err)
		}
		if disabled.Valid {
			return status, nil
		}
		secret, tok, err := insertToken(tx, cur.approvedApp.String, cur.scopes, nil, nil)
		if err != nil {
			return status, err
		}
		detail := map[string]any{"app_id": cur.approvedApp.String, "token_id": tok.ID, "scopes": cur.scopes, "pairing_request": cur.id}
		if _, err := audit.Write(tx, audit.System.Entry(ActionTokenCreate, "app_token", tok.ID, detail)); err != nil {
			return status, err
		}
		if cur.replaced {
			if revoked, err = revokeOtherTokens(tx, audit.System, cur.approvedApp.String, botID, tok.ID, cur.id); err != nil {
				return status, err
			}
		}
		if _, err := tx.Exec(`UPDATE pairing_requests SET state = 'claimed' WHERE id = ?`, cur.id); err != nil {
			return status, fmt.Errorf("claim pairing request: %w", err)
		}
		channels, err := botChannels(tx.Context(), tx, botID)
		if err != nil {
			return status, err
		}
		return protocol.PairingStatus{State: protocol.PairingClaimed, Token: secret, ExpiresAt: status.ExpiresAt, Channels: channels}, nil
	})
	if err == nil {
		for _, tokenID := range revoked {
			s.forget(tokenID)
		}
	}
	return st, err
}

// revokeOtherTokens revokes every unrevoked token of the app except keep
// (empty keeps none) for pairing request reqID: at its claim, keep is the
// token the claim just minted. Each revocation is audited like RevokeToken
// by actor and closes that token's live connections when the transaction
// commits. It returns the revoked token ids, which the caller drops from the
// auth cache after the commit.
func revokeOtherTokens(tx *store.Tx, actor audit.Actor, appID, botID, keep, reqID string) ([]string, error) {
	rows, err := tx.QueryContext(tx.Context(), `SELECT id, prefix FROM app_tokens
		WHERE app_id = ? AND id != ? AND revoked_at IS NULL ORDER BY id`, appID, keep)
	if err != nil {
		return nil, fmt.Errorf("list replaced tokens: %w", err)
	}
	type old struct{ id, prefix string }
	var olds []old
	for rows.Next() {
		var o old
		if err := rows.Scan(&o.id, &o.prefix); err != nil {
			rows.Close()
			return nil, fmt.Errorf("list replaced tokens: %w", err)
		}
		olds = append(olds, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list replaced tokens: %w", err)
	}
	ids := make([]string, 0, len(olds))
	for _, o := range olds {
		if _, err := tx.Exec(`UPDATE app_tokens SET revoked_at = ? WHERE id = ?`, tx.Now(), o.id); err != nil {
			return nil, fmt.Errorf("revoke replaced token: %w", err)
		}
		if err := appendBearerClose(tx, botID, map[string]any{"token_id": o.id, "app_id": appID}); err != nil {
			return nil, err
		}
		detail := map[string]string{"app_id": appID, "prefix": o.prefix, "pairing_request": reqID}
		if _, err := audit.Write(tx, actor.Entry(ActionTokenRevoke, "app_token", o.id, detail)); err != nil {
			return nil, err
		}
		ids = append(ids, o.id)
	}
	return ids, nil
}

// botChannels lists the public and private channels, not archived, that the
// bot userID is a member of, sorted by name then id. It is nil when there
// are none, so the claim answer leaves the field out.
func botChannels(ctx context.Context, q queryer, userID string) ([]protocol.PairingChannel, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.id, c.name FROM memberships m JOIN channels c ON c.id = m.channel_id
		WHERE m.user_id = ? AND c.kind IN ('public', 'private') AND c.archived_at IS NULL ORDER BY c.name, c.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list bot channels: %w", err)
	}
	defer rows.Close()
	var out []protocol.PairingChannel
	for rows.Next() {
		var ch protocol.PairingChannel
		if err := rows.Scan(&ch.ID, &ch.Name); err != nil {
			return nil, fmt.Errorf("list bot channels: %w", err)
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// ListPending returns the open pairing requests, oldest first, each marked
// with caFingerprint (the one the app shows, for the admin to compare).
func (s *Service) ListPending(ctx context.Context, caFingerprint string) ([]protocol.PendingPairing, error) {
	rows, err := s.db.Read().QueryContext(ctx, `SELECT `+pairingCols+` FROM pairing_requests
		WHERE state = 'pending' AND expires_at > ? ORDER BY created_at, id`, s.now().UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("list pairing requests: %w", err)
	}
	var found []pairingRow
	for rows.Next() {
		r, err := scanPairing(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("list pairing requests: %w", err)
		}
		found = append(found, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list pairing requests: %w", err)
	}
	out := make([]protocol.PendingPairing, 0, len(found))
	for _, r := range found {
		if r.scopes == nil {
			r.scopes = []string{}
		}
		p := protocol.PendingPairing{
			ID: r.id, AppName: r.appName, Description: r.description, RequestedScopes: r.scopes, Code: r.code,
			SourceIP: r.sourceIP, CAFingerprint: caFingerprint,
			CreatedAt: protocol.FormatTime(r.created), ExpiresAt: protocol.FormatTime(r.expires),
		}
		if r.replacesApp.Valid {
			if p.Replaces, err = loadReplaces(ctx, s.db.Read(), r.replacesApp.String); err != nil {
				return nil, err
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// loadReplaces describes the existing app a pairing request names, or
// returns nil when there is no such app. An app whose bot account is
// deactivated is nil too: it cannot be reconnected (reconnectApp refuses it,
// and no route reactivates a bot account), so the admin sees the request as
// a new app instead of a reconnect box that could only fail.
func loadReplaces(ctx context.Context, q queryer, appID string) (*protocol.PairingReplaces, error) {
	var (
		r                     protocol.PairingReplaces
		botID                 string
		disabled, deactivated sql.NullInt64
	)
	err := q.QueryRowContext(ctx, `SELECT a.id, a.name, u.username, a.disabled_at, a.user_id, u.deactivated_at
		FROM apps a JOIN users u ON u.id = a.user_id WHERE a.id = ?`, appID).Scan(&r.AppID, &r.Name, &r.Username, &disabled, &botID, &deactivated)
	if isNoRows(err) || (err == nil && deactivated.Valid) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load replaced app: %w", err)
	}
	r.DisabledAt = nullMs(disabled)
	if r.Channels, err = botChannels(ctx, q, botID); err != nil {
		return nil, err
	}
	if r.Channels == nil {
		r.Channels = []protocol.PairingChannel{}
	}
	return &r, nil
}

// ApprovePairing creates the app and its bot for a pending request, adds the
// bot to the chosen channels (and, when asked, to the public
// "<username>-alerts" channel, created unless it exists), and records the
// approved scopes on the request. No token is created here: the app's first
// poll after the approval mints it (see PollPairing). admin must be an
// admin; audit actor is the admin too.
//
// With ReplaceApp the request must name an existing app, which is reconnected
// instead: no app or bot is created, the app keeps its id, bot, username and
// channel memberships, takes the request's description and is enabled again
// if it was disabled. The tokens of an enabled app stay as they are until
// the claim, which revokes all but the new one. A disabled app's tokens are
// revoked by the approval itself (see reconnectApp), so enabling it revives
// none of them.
func (s *Service) ApprovePairing(ctx context.Context, admin chat.Actor, aud audit.Actor, id string, in protocol.ApprovePairingRequest) (protocol.PairingApproval, error) {
	var revoked []string
	res, err := run(ctx, s, func(tx *store.Tx) (protocol.PairingApproval, error) {
		revoked = nil
		now := tx.Now()
		if err := expireStale(tx, now); err != nil {
			return protocol.PairingApproval{}, err
		}
		row, err := loadPairing(tx.Context(), tx, id)
		if err != nil {
			return protocol.PairingApproval{}, err
		}
		if row.state != protocol.PairingPending {
			return protocol.PairingApproval{}, conflict("this pairing request is " + row.state)
		}
		scopes := in.Scopes
		if len(scopes) == 0 {
			scopes = row.scopes
		}
		scopes, err = NormalizeScopes("scopes", scopes)
		if err != nil {
			return protocol.PairingApproval{}, err
		}
		if len(scopes) == 0 {
			return protocol.PairingApproval{}, invalid("scopes", "at least one scope is required")
		}
		channels, err := s.checkChannels(tx, in.ChannelIDs)
		if err != nil {
			return protocol.PairingApproval{}, err
		}
		var app protocol.App
		if in.ReplaceApp {
			app, revoked, err = reconnectApp(tx, aud, row)
		} else {
			app, err = s.CreateAppTx(tx, aud, CreateAppInput{Name: row.appName, Description: row.description, OwnerID: admin.ID})
		}
		if err != nil {
			return protocol.PairingApproval{}, err
		}
		added := make([]string, 0, len(channels)+1)
		if in.CreateChannel {
			chID, err := s.alertsChannel(tx, admin, app)
			if err != nil {
				return protocol.PairingApproval{}, err
			}
			added = append(added, chID)
		}
		for _, chID := range channels {
			if slices.Contains(added, chID) {
				continue
			}
			// Adding a bot that is already a member (a replaced app) changes nothing.
			if _, err := s.chat.AddMemberTx(tx, admin, chID, app.UserID, protocol.MemberMember); err != nil {
				return protocol.PairingApproval{}, err
			}
			added = append(added, chID)
		}
		scopesJSON, err := json.Marshal(scopes)
		if err != nil {
			return protocol.PairingApproval{}, fmt.Errorf("encode scopes: %w", err)
		}
		// requested_scopes now holds the approved scopes, which the claim
		// turns into the token. The channels live in the bot's memberships.
		claimBy := max(row.expires, now+ClaimWindow.Milliseconds())
		_, err = tx.Exec(`UPDATE pairing_requests SET state = 'approved', approved_app_id = ?, requested_scopes = ?, expires_at = ?, replaced = ?
			WHERE id = ?`, app.ID, string(scopesJSON), claimBy, boolInt(in.ReplaceApp), row.id)
		if err != nil {
			return protocol.PairingApproval{}, fmt.Errorf("approve pairing request: %w", err)
		}
		detail := map[string]any{"app_id": app.ID, "scopes": scopes, "channel_ids": added}
		if in.ReplaceApp {
			detail["replaced_app"] = true
		}
		if _, err := audit.Write(tx, aud.Entry(ActionPairingApprove, "pairing_request", row.id, detail)); err != nil {
			return protocol.PairingApproval{}, err
		}
		app, err = loadApp(tx.Context(), tx, app.ID)
		if err != nil {
			return protocol.PairingApproval{}, err
		}
		return protocol.PairingApproval{App: app, ChannelIDs: added}, nil
	})
	if err == nil {
		for _, tokenID := range revoked {
			s.forget(tokenID)
		}
	}
	return res, err
}

// reconnectApp prepares the existing app a pending request names for a
// replace approval: it takes the request's description and is enabled again
// if it was disabled (the admin chose to reconnect it; audited as
// app.enable). A disabled app's tokens did not work, and an admin may have
// disabled it because one leaked, so they are revoked here (audited as
// token.revoke by the admin) instead of at the claim: enabling the app
// revives none of them, even when the approval is never claimed. It returns
// the app and the ids of the tokens it revoked. A request that names no
// existing app, or an app whose bot account is deactivated, is a conflict.
func reconnectApp(tx *store.Tx, aud audit.Actor, row pairingRow) (protocol.App, []string, error) {
	ctx := tx.Context()
	if !row.replacesApp.Valid {
		return protocol.App{}, nil, conflict("this pairing request does not name an existing app to reconnect; approve it as a new app")
	}
	app, err := loadApp(ctx, tx, row.replacesApp.String)
	if errors.Is(err, chat.ErrNotFound) {
		return protocol.App{}, nil, conflict("the app this pairing request names no longer exists; approve it as a new app")
	}
	if err != nil {
		return protocol.App{}, nil, err
	}
	var deactivated sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT deactivated_at FROM users WHERE id = ?`, app.UserID).Scan(&deactivated); err != nil {
		return protocol.App{}, nil, fmt.Errorf("load bot user: %w", err)
	}
	if deactivated.Valid {
		return protocol.App{}, nil, conflict("the bot account of " + app.Name + " is deactivated, so it cannot be reconnected; approve the request as a new app")
	}
	if _, err := tx.Exec(`UPDATE apps SET description = ? WHERE id = ?`, row.description, app.ID); err != nil {
		return protocol.App{}, nil, fmt.Errorf("update replaced app: %w", err)
	}
	var revoked []string
	if app.DisabledAt != nil {
		if revoked, err = revokeOtherTokens(tx, aud, app.ID, app.UserID, "", row.id); err != nil {
			return protocol.App{}, nil, err
		}
		if _, err := tx.Exec(`UPDATE apps SET disabled_at = NULL WHERE id = ?`, app.ID); err != nil {
			return protocol.App{}, nil, fmt.Errorf("enable replaced app: %w", err)
		}
		detail := map[string]string{"name": app.Name, "pairing_request": row.id}
		if _, err := audit.Write(tx, aud.Entry(ActionAppEnable, "app", app.ID, detail)); err != nil {
			return protocol.App{}, nil, err
		}
	}
	app, err = loadApp(ctx, tx, app.ID)
	return app, revoked, err
}

// alertsChannel adds the bot of app to its public "<username>-alerts"
// channel and returns the channel's id. The channel is created when no
// public or private channel has that name. An existing public channel that
// is not archived is reused (adding a bot that is already a member changes
// nothing); a private or archived one is a conflict, since the bot must not
// be added to it behind the admin's back.
func (s *Service) alertsChannel(tx *store.Tx, admin chat.Actor, app protocol.App) (string, error) {
	name := alertsChannelName(app.Username)
	var (
		chID, kind string
		archived   sql.NullInt64
	)
	err := tx.QueryRowContext(tx.Context(), `SELECT id, kind, archived_at FROM channels WHERE name = ? AND kind IN ('public', 'private')
		ORDER BY id LIMIT 1`, name).Scan(&chID, &kind, &archived)
	switch {
	case isNoRows(err):
		ch, err := s.chat.CreateChannelTx(tx, admin, chat.CreateChannelParams{
			Name: name, Kind: protocol.ChannelPublic, Topic: "Alerts from " + app.Name, MemberIDs: []string{app.UserID},
		})
		if err != nil {
			return "", err
		}
		return ch.ID, nil
	case err != nil:
		return "", fmt.Errorf("find alerts channel: %w", err)
	case kind != protocol.ChannelPublic:
		return "", conflict("a private channel named #" + name + " already exists; tick it in the channel list instead of creating one")
	case archived.Valid:
		return "", conflict("an archived channel named #" + name + " already exists; unarchive or rename it, or do not create one")
	}
	if _, err := s.chat.AddMemberTx(tx, admin, chID, app.UserID, protocol.MemberMember); err != nil {
		return "", err
	}
	return chID, nil
}

// CancelPairing withdraws a request for the app that opened it: the poll
// secret must match, else the request is ErrNotFound, exactly as for
// PollPairing (whose per-address limit it shares). A pending request becomes
// cancelled. So does an approved one the app has not claimed yet; the app
// created for it is then disabled, as when such an approval expires, while
// the app of a replaced request is left as it is. Cancelling a cancelled
// request does nothing; a claimed, denied or expired one is a conflict.
func (s *Service) CancelPairing(ctx context.Context, ip, id, secret string) error {
	if _, err := s.pollerRequest(ctx, ip, id, secret); err != nil {
		return err
	}
	_, err := run(ctx, s, func(tx *store.Tx) (struct{}, error) {
		now := tx.Now()
		if err := expireStale(tx, now); err != nil {
			return struct{}{}, err
		}
		row, err := loadPairing(tx.Context(), tx, id)
		if err != nil {
			return struct{}{}, err
		}
		detail := map[string]any{"app_name": row.appName, "state": row.state}
		switch row.state {
		case protocol.PairingCancelled:
			return struct{}{}, nil
		case protocol.PairingPending:
		case protocol.PairingApproved:
			if row.approvedApp.Valid && !row.replaced {
				if _, err := tx.Exec(`UPDATE apps SET disabled_at = ? WHERE id = ? AND disabled_at IS NULL`, now, row.approvedApp.String); err != nil {
					return struct{}{}, fmt.Errorf("disable cancelled pairing app: %w", err)
				}
				detail["app_disabled"] = true
			}
			if row.approvedApp.Valid {
				detail["app_id"] = row.approvedApp.String
			}
		default:
			return struct{}{}, conflict("this pairing request is " + row.state)
		}
		if _, err := tx.Exec(`UPDATE pairing_requests SET state = 'cancelled' WHERE id = ?`, row.id); err != nil {
			return struct{}{}, fmt.Errorf("cancel pairing request: %w", err)
		}
		_, err = audit.Write(tx, audit.System.Entry(ActionPairingCancel, "pairing_request", row.id, detail))
		return struct{}{}, err
	})
	return err
}

// DenyPairing rejects a pending request.
func (s *Service) DenyPairing(ctx context.Context, aud audit.Actor, id string) error {
	_, err := run(ctx, s, func(tx *store.Tx) (struct{}, error) {
		if err := expireStale(tx, tx.Now()); err != nil {
			return struct{}{}, err
		}
		row, err := loadPairing(tx.Context(), tx, id)
		if err != nil {
			return struct{}{}, err
		}
		if row.state != protocol.PairingPending {
			return struct{}{}, conflict("this pairing request is " + row.state)
		}
		if _, err := tx.Exec(`UPDATE pairing_requests SET state = 'denied' WHERE id = ?`, row.id); err != nil {
			return struct{}{}, fmt.Errorf("deny pairing request: %w", err)
		}
		_, err = audit.Write(tx, aud.Entry(ActionPairingDeny, "pairing_request", row.id, map[string]string{"app_name": row.appName}))
		return struct{}{}, err
	})
	return err
}

// alertsChannelName returns the name of the alerts channel of a bot.
func alertsChannelName(username string) string {
	return strings.NewReplacer(".", "-", "_", "-").Replace(username) + "-alerts"
}
