// Package hooks is the incoming-webhook domain (plan pipeline step 12): a
// secret URL, POST /hooks/{token}, that posts into one channel as a
// pseudo-app. It accepts the native payload ({"text", "username"?, "alert"?})
// and a subset of the Slack webhook format (text, username, icon_emoji,
// attachments, Block Kit blocks), as JSON or as a form with a payload= field,
// and translates it to safe Markdown.
//
// A webhook is a webhooks row plus an apps row and a bot user created with it
// (the pseudo-app), which authors the messages and owns the alerts. The secret
// is shown once; only its SHA-256 is stored. Every message it posts is stored
// with mentions switched off, so a payload can never notify anyone, and its
// username is only a display name carried in the message.
package hooks

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donaldsteele/bubbles/internal/alerts"
	"github.com/donaldsteele/bubbles/internal/apps"
	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Audit actions written by this package. Detail never carries the token.
const (
	ActionWebhookCreate = "webhook.create"
	ActionWebhookRevoke = "webhook.revoke"
)

// Rate limit of one webhook: 30 posts per minute, burst 10.
const (
	PostsPerMinute = 30
	PostBurst      = 10
)

const maxTokenLen = 128

// Options configure a Service. All fields but Now are required.
type Options struct {
	DB     *store.DB
	Chat   *chat.Service
	Apps   *apps.Service
	Alerts *alerts.Service
	// Now is the clock of the rate limit; nil means time.Now.
	Now func() time.Time
}

// Service is the webhooks domain service.
type Service struct {
	db     *store.DB
	chat   *chat.Service
	apps   *apps.Service
	alerts *alerts.Service
	limit  *auth.BucketLimiter
}

// New returns a Service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{
		db: o.DB, chat: o.Chat, apps: o.Apps, alerts: o.Alerts,
		limit: auth.NewBucketLimiter(o.Now, PostsPerMinute/60.0, PostBurst),
	}
}

func notFound() error { return &chat.Error{Kind: chat.ErrNotFound, Msg: "not found"} }

func invalid(field, reason string) error {
	return &chat.ValidationError{Fields: map[string]string{field: reason}}
}

// Allow takes one post from the webhook's bucket and reports the wait when the
// bucket is empty.
func (s *Service) Allow(webhookID string) (bool, time.Duration) { return s.limit.Allow(webhookID) }

// CreateInput are the inputs of Create.
type CreateInput struct {
	ChannelID string
	Name      string
}

// Create makes a webhook for a public or private channel: a pseudo-app named
// name with a bot user that joins the channel, and a secret token. admin must
// be an admin (the bot is added to the channel as one). It returns the webhook
// and the token, which is not recoverable afterwards.
func (s *Service) Create(ctx context.Context, admin chat.Actor, aud audit.Actor, in CreateInput) (protocol.Webhook, string, error) {
	secret, err := auth.NewToken()
	if err != nil {
		return protocol.Webhook{}, "", err
	}
	token := protocol.WebhookTokenPrefix + secret
	var out protocol.Webhook
	err = s.db.Do(ctx, func(tx *store.Tx) error {
		var kind string
		var archived sql.NullInt64
		err := tx.QueryRowContext(tx.Context(), `SELECT kind, archived_at FROM channels WHERE id = ?`, in.ChannelID).Scan(&kind, &archived)
		switch {
		case errors.Is(err, sql.ErrNoRows) || (err == nil && kind != protocol.ChannelPublic && kind != protocol.ChannelPrivate):
			return invalid("channel_id", "no such channel")
		case err != nil:
			return fmt.Errorf("load channel: %w", err)
		case archived.Valid:
			return invalid("channel_id", "the channel is archived")
		}
		app, err := s.apps.CreateAppTx(tx, aud, apps.CreateAppInput{
			Name: in.Name, Description: "Incoming webhook", OwnerID: admin.ID,
		})
		if err != nil {
			return err
		}
		if _, err := s.chat.AddMemberTx(tx, admin, in.ChannelID, app.UserID, protocol.MemberMember); err != nil {
			if errors.Is(err, chat.ErrNotFound) {
				// A private channel the admin is not in does not exist for them.
				return invalid("channel_id", "no such channel")
			}
			return err
		}
		id, now := tx.NewID(), tx.Now()
		_, err = tx.Exec(`INSERT INTO webhooks (id, channel_id, app_id, name, token_hash, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, in.ChannelID, app.ID, app.Name, auth.HashToken(token), nullStr(admin.ID), now)
		if err != nil {
			return fmt.Errorf("insert webhook: %w", err)
		}
		detail := map[string]string{"name": app.Name, "channel_id": in.ChannelID, "app_id": app.ID}
		if _, err := audit.Write(tx, aud.Entry(ActionWebhookCreate, "webhook", id, detail)); err != nil {
			return err
		}
		out = protocol.Webhook{
			ID: id, ChannelID: in.ChannelID, AppID: app.ID, Name: app.Name,
			CreatedBy: nonEmpty(admin.ID), CreatedAt: protocol.FormatTime(now),
		}
		return nil
	})
	if err != nil {
		return protocol.Webhook{}, "", err
	}
	return out, token, nil
}

// List returns every webhook, revoked ones included, oldest first. It never
// returns a token.
func (s *Service) List(ctx context.Context) ([]protocol.Webhook, error) {
	rows, err := s.db.Read().QueryContext(ctx, `SELECT id, channel_id, app_id, name, created_by, created_at, revoked_at
		FROM webhooks ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	defer rows.Close()
	out := []protocol.Webhook{}
	for rows.Next() {
		var (
			w       protocol.Webhook
			by      sql.NullString
			created int64
			revoked sql.NullInt64
		)
		if err := rows.Scan(&w.ID, &w.ChannelID, &w.AppID, &w.Name, &by, &created, &revoked); err != nil {
			return nil, fmt.Errorf("list webhooks: %w", err)
		}
		if by.Valid {
			w.CreatedBy = &by.String
		}
		w.CreatedAt = protocol.FormatTime(created)
		if revoked.Valid {
			t := protocol.FormatTime(revoked.Int64)
			w.RevokedAt = &t
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Revoke ends a webhook: its URL answers 404 from now on and its pseudo-app is
// disabled. Revoking a revoked webhook changes nothing.
func (s *Service) Revoke(ctx context.Context, aud audit.Actor, id string) error {
	return s.db.Do(ctx, func(tx *store.Tx) error {
		var (
			channelID, appID, name string
			revoked                sql.NullInt64
		)
		err := tx.QueryRowContext(tx.Context(), `SELECT channel_id, app_id, name, revoked_at FROM webhooks WHERE id = ?`, id).
			Scan(&channelID, &appID, &name, &revoked)
		if errors.Is(err, sql.ErrNoRows) {
			return notFound()
		}
		if err != nil {
			return fmt.Errorf("load webhook: %w", err)
		}
		if revoked.Valid {
			return nil
		}
		now := tx.Now()
		if _, err := tx.Exec(`UPDATE webhooks SET revoked_at = ? WHERE id = ?`, now, id); err != nil {
			return fmt.Errorf("revoke webhook: %w", err)
		}
		if _, err := tx.Exec(`UPDATE apps SET disabled_at = ? WHERE id = ? AND disabled_at IS NULL`, now, appID); err != nil {
			return fmt.Errorf("disable pseudo-app: %w", err)
		}
		detail := map[string]string{"name": name, "channel_id": channelID, "app_id": appID}
		_, err = audit.Write(tx, aud.Entry(ActionWebhookRevoke, "webhook", id, detail))
		return err
	})
}

// Target is the webhook a token opens.
type Target struct {
	ID        string
	ChannelID string
	AppID     string
	// BotUserID is the pseudo-app's bot account, the author of every message.
	BotUserID string
	Name      string
}

// Lookup finds the live webhook for a path token. Anything wrong with it is
// ErrNotFound: the token is unknown or malformed, the webhook is revoked, its
// pseudo-app is disabled or its bot deactivated, or the bot left the channel.
func (s *Service) Lookup(ctx context.Context, token string) (Target, error) {
	if !strings.HasPrefix(token, protocol.WebhookTokenPrefix) || len(token) > maxTokenLen {
		return Target{}, notFound()
	}
	hash := auth.HashToken(token)
	var (
		t      Target
		stored []byte
	)
	err := s.db.Read().QueryRowContext(ctx, `SELECT w.id, w.channel_id, w.app_id, a.user_id, w.name, w.token_hash
		FROM webhooks w
		JOIN apps a ON a.id = w.app_id
		JOIN users u ON u.id = a.user_id
		WHERE w.token_hash = ? AND w.revoked_at IS NULL AND a.disabled_at IS NULL AND u.deactivated_at IS NULL
		  AND EXISTS (SELECT 1 FROM memberships m WHERE m.channel_id = w.channel_id AND m.user_id = a.user_id)`, hash).
		Scan(&t.ID, &t.ChannelID, &t.AppID, &t.BotUserID, &t.Name, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return Target{}, notFound()
	}
	if err != nil {
		return Target{}, fmt.Errorf("find webhook: %w", err)
	}
	if subtle.ConstantTimeCompare(stored, hash) != 1 {
		return Target{}, notFound()
	}
	return t, nil
}

// Result is what a delivery did.
type Result struct {
	// Alert is set when the payload was an alert; Transition is then what the
	// alerts service did with it.
	Alert      bool
	Transition string
}

// Deliver posts a translated payload for the webhook. Text goes in as a chat
// message authored by the pseudo-app with the sender name as display override;
// an alert goes through the alerts service with the pseudo-app as its source.
// Neither can mention anyone.
func (s *Service) Deliver(ctx context.Context, t Target, m Message) (Result, error) {
	if m.Alert != nil {
		in := *m.Alert
		in.Channel = t.ChannelID
		actor := chat.Actor{
			ID: t.BotUserID, Role: protocol.RoleMember, Kind: protocol.KindApp,
			Scopes: []string{protocol.ScopeAlertsWrite}, ChannelIDs: []string{t.ChannelID},
		}
		res, err := s.alerts.PostWith(ctx, actor, in, alerts.PostOptions{DisableMentions: true})
		var ve *chat.ValidationError
		if errors.As(err, &ve) {
			fields := make(map[string]string, len(ve.Fields))
			for k, v := range ve.Fields {
				fields["alert."+k] = v
			}
			err = &chat.ValidationError{Fields: fields}
		}
		return Result{Alert: true, Transition: res.Transition}, err
	}
	err := s.db.Do(ctx, func(tx *store.Tx) error {
		_, err := s.chat.CreateMessageTx(tx, chat.CreateMessageParams{
			ChannelID: t.ChannelID, AuthorID: t.BotUserID, Kind: protocol.MessageText,
			Body: m.Body, OverrideName: m.Username, DisableMentions: true,
		})
		return err
	})
	return Result{}, err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
