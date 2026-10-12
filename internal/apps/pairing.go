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
		id, expires := tx.NewID(), now+PairingTTL.Milliseconds()
		_, err = tx.Exec(`INSERT INTO pairing_requests (id, app_name, description, requested_scopes, code, poll_secret_hash, source_ip, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, name, desc, string(scopesJSON), code, auth.HashToken(secret), ip, now, expires)
		if err != nil {
			return protocol.PairingCreated{}, fmt.Errorf("insert pairing request: %w", err)
		}
		return protocol.PairingCreated{ID: id, PollSecret: secret, Code: code, ExpiresAt: protocol.FormatTime(expires)}, nil
	})
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
// expired. An approved request the app never claimed also disables that app:
// it was created for the request and never received a token, so it is
// switched off (not deleted, which keeps the audit trail intact).
func expireStale(tx *store.Tx, now int64) error {
	if _, err := tx.Exec(`UPDATE apps SET disabled_at = ? WHERE disabled_at IS NULL AND id IN
		(SELECT approved_app_id FROM pairing_requests WHERE state = 'approved' AND expires_at <= ? AND approved_app_id IS NOT NULL)`, now, now); err != nil {
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
	approvedApp                                     sql.NullString
	created, expires                                int64
}

// pairingCols never selects token_ciphertext: no token material is stored
// for a pairing request, so there is nothing to read back.
const pairingCols = `id, app_name, description, requested_scopes, code, poll_secret_hash, source_ip, state, approved_app_id,
	created_at, expires_at`

func loadPairing(ctx context.Context, q queryer, id string) (pairingRow, error) {
	var (
		r      pairingRow
		scopes string
	)
	err := q.QueryRowContext(ctx, `SELECT `+pairingCols+` FROM pairing_requests WHERE id = ?`, id).
		Scan(&r.id, &r.appName, &r.description, &scopes, &r.code, &r.secretHash, &r.sourceIP, &r.state, &r.approvedApp,
			&r.created, &r.expires)
	if errors.Is(err, sql.ErrNoRows) {
		return r, notFound()
	}
	if err != nil {
		return r, fmt.Errorf("load pairing request: %w", err)
	}
	if r.scopes, err = decodeStrings(scopes); err != nil {
		return r, fmt.Errorf("pairing scopes: %w", err)
	}
	return r, nil
}

// PollPairing answers the app's poll. The poll secret must match, else the
// request is ErrNotFound (a wrong secret reveals nothing, not even that the
// id exists). No token exists before the first poll after the approval: that
// poll mints it in the same transaction that moves the request to claimed, so
// the token is returned once and never stored in any recoverable form.
func (s *Service) PollPairing(ctx context.Context, ip, id, secret string) (protocol.PairingStatus, error) {
	if ok, wait := s.pairPoll.Allow(auth.IPKey(ip)); !ok {
		return protocol.PairingStatus{}, &auth.RateLimitedError{RetryAfter: wait}
	}
	if id == "" || len(id) > 64 || secret == "" || len(secret) > 128 {
		return protocol.PairingStatus{}, notFound()
	}
	row, err := loadPairing(ctx, s.db.Read(), id)
	if err != nil {
		return protocol.PairingStatus{}, err
	}
	if subtle.ConstantTimeCompare(row.secretHash, auth.HashToken(secret)) != 1 {
		return protocol.PairingStatus{}, notFound()
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
	return run(ctx, s, func(tx *store.Tx) (protocol.PairingStatus, error) {
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
		var disabled sql.NullInt64
		err = tx.QueryRowContext(tx.Context(), `SELECT disabled_at FROM apps WHERE id = ?`, cur.approvedApp.String).Scan(&disabled)
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
		if _, err := tx.Exec(`UPDATE pairing_requests SET state = 'claimed' WHERE id = ?`, cur.id); err != nil {
			return status, fmt.Errorf("claim pairing request: %w", err)
		}
		return protocol.PairingStatus{State: protocol.PairingClaimed, Token: secret, ExpiresAt: status.ExpiresAt}, nil
	})
}

// ListPending returns the open pairing requests, oldest first, each marked
// with caFingerprint (the one the app shows, for the admin to compare).
func (s *Service) ListPending(ctx context.Context, caFingerprint string) ([]protocol.PendingPairing, error) {
	rows, err := s.db.Read().QueryContext(ctx, `SELECT `+pairingCols+` FROM pairing_requests
		WHERE state = 'pending' AND expires_at > ? ORDER BY created_at, id`, s.now().UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("list pairing requests: %w", err)
	}
	defer rows.Close()
	out := []protocol.PendingPairing{}
	for rows.Next() {
		var (
			r      pairingRow
			scopes string
		)
		err := rows.Scan(&r.id, &r.appName, &r.description, &scopes, &r.code, &r.secretHash, &r.sourceIP, &r.state, &r.approvedApp,
			&r.created, &r.expires)
		if err != nil {
			return nil, fmt.Errorf("list pairing requests: %w", err)
		}
		if r.scopes, err = decodeStrings(scopes); err != nil {
			return nil, fmt.Errorf("pairing scopes: %w", err)
		}
		if r.scopes == nil {
			r.scopes = []string{}
		}
		out = append(out, protocol.PendingPairing{
			ID: r.id, AppName: r.appName, Description: r.description, RequestedScopes: r.scopes, Code: r.code,
			SourceIP: r.sourceIP, CAFingerprint: caFingerprint,
			CreatedAt: protocol.FormatTime(r.created), ExpiresAt: protocol.FormatTime(r.expires),
		})
	}
	return out, rows.Err()
}

// ApprovePairing creates the app, its bot and its first token for a pending
// request, adds the bot to the chosen channels (and to a new public
// "<app>-alerts" channel when asked), and records the approved scopes on the
// request. No token is created here: the app's first poll after the approval
// mints it (see PollPairing). admin must be an admin; audit actor is the admin too.
func (s *Service) ApprovePairing(ctx context.Context, admin chat.Actor, aud audit.Actor, id string, in protocol.ApprovePairingRequest) (protocol.PairingApproval, error) {
	return run(ctx, s, func(tx *store.Tx) (protocol.PairingApproval, error) {
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
		app, err := s.CreateAppTx(tx, aud, CreateAppInput{Name: row.appName, Description: row.description, OwnerID: admin.ID})
		if err != nil {
			return protocol.PairingApproval{}, err
		}
		added := make([]string, 0, len(channels)+1)
		if in.CreateChannel {
			ch, err := s.chat.CreateChannelTx(tx, admin, chat.CreateChannelParams{
				Name: alertsChannelName(app.Username), Kind: protocol.ChannelPublic,
				Topic: "Alerts from " + app.Name, MemberIDs: []string{app.UserID},
			})
			if err != nil {
				return protocol.PairingApproval{}, err
			}
			added = append(added, ch.ID)
		}
		for _, chID := range channels {
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
		_, err = tx.Exec(`UPDATE pairing_requests SET state = 'approved', approved_app_id = ?, requested_scopes = ?, expires_at = ? WHERE id = ?`,
			app.ID, string(scopesJSON), claimBy, row.id)
		if err != nil {
			return protocol.PairingApproval{}, fmt.Errorf("approve pairing request: %w", err)
		}
		detail := map[string]any{"app_id": app.ID, "scopes": scopes, "channel_ids": added}
		if _, err := audit.Write(tx, aud.Entry(ActionPairingApprove, "pairing_request", row.id, detail)); err != nil {
			return protocol.PairingApproval{}, err
		}
		app, err = loadApp(tx.Context(), tx, app.ID)
		if err != nil {
			return protocol.PairingApproval{}, err
		}
		return protocol.PairingApproval{App: app, ChannelIDs: added}, nil
	})
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
