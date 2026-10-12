package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// notice publishes an ephemeral.message (seq 0, not stored) to the
// connections of userID only. It is best effort: a failure to encode is
// logged, never returned.
func (s *Service) notice(userID, channelID string, thread *string, invocationID string, author *protocol.Author, body string) {
	payload, err := json.Marshal(protocol.EphemeralMessage{
		InvocationID: invocationID, ChannelID: channelID, ThreadRootID: thread, Author: author, Body: body,
	})
	if err != nil {
		s.logger.Warn("commands: encode ephemeral message", "err", err)
		return
	}
	s.rt.Publish(events.Event{Type: events.TypeEphemeralMessage, UserID: userID, Payload: payload, CreatedAt: s.now().UnixMilli()})
}

// invocationFor returns the live invocation id if it belongs to the token's
// app, else notFound (unknown, expired, foreign or outside the allowlist).
func (s *Service) invocationFor(g *auth.TokenGrant, id string) (invocation, error) {
	s.invMu.Lock()
	defer s.invMu.Unlock()
	inv, ok := s.invocations[id]
	if !ok {
		return invocation{}, notFound()
	}
	if inv.expires <= s.now().UnixMilli() {
		delete(s.invocations, id)
		return invocation{}, notFound()
	}
	if inv.appID != g.AppID || (g.ChannelIDs != nil && !slices.Contains(g.ChannelIDs, inv.channelID)) {
		return invocation{}, notFound()
	}
	return *inv, nil
}

// Respond answers an invocation as the app. An ephemeral answer reaches only
// the invoker's connections and is not stored; any other is a normal message
// of the app's bot in the channel (and thread) the command ran in, with
// mentions disabled.
func (s *Service) Respond(ctx context.Context, g *auth.TokenGrant, id string, req protocol.RespondCommandRequest) (protocol.RespondedCommand, error) {
	if err := requireScope(g, ScopeCommands); err != nil {
		return protocol.RespondedCommand{}, err
	}
	inv, err := s.invocationFor(g, id)
	if err != nil {
		return protocol.RespondedCommand{}, err
	}
	text := strings.TrimSpace(req.Text)
	switch {
	case text == "":
		return protocol.RespondedCommand{}, invalid("text", "text is required")
	case utf8.RuneCountInString(text) > protocol.MaxCommandRespondChars || !validText(text):
		return protocol.RespondedCommand{}, invalid("text", fmt.Sprintf("text must be valid text of at most %d characters", protocol.MaxCommandRespondChars))
	}
	if req.Ephemeral {
		author := protocol.Author{ID: inv.botID, Kind: protocol.KindApp}
		err := s.db.Read().QueryRowContext(ctx, `SELECT username, display_name FROM users WHERE id = ?`, inv.botID).Scan(&author.Username, &author.DisplayName)
		if err != nil {
			return protocol.RespondedCommand{}, fmt.Errorf("load bot: %w", err)
		}
		s.notice(inv.invokerID, inv.channelID, inv.threadRootID, id, &author, text)
		return protocol.RespondedCommand{Ephemeral: true}, nil
	}
	thread := ""
	if inv.threadRootID != nil {
		thread = *inv.threadRootID
	}
	msg, err := run(ctx, s, func(tx *store.Tx) (protocol.Message, error) {
		var one int
		err := tx.QueryRowContext(tx.Context(), `SELECT 1 FROM memberships WHERE channel_id = ? AND user_id = ?`, inv.channelID, inv.botID).Scan(&one)
		if isNoRows(err) {
			return protocol.Message{}, conflict("the app is no longer a member of the channel")
		}
		if err != nil {
			return protocol.Message{}, fmt.Errorf("check app membership: %w", err)
		}
		return s.chat.CreateMessageTx(tx, chat.CreateMessageParams{
			ChannelID: inv.channelID, AuthorID: inv.botID, Body: text, ThreadRootID: thread, DisableMentions: true,
		})
	})
	if err != nil {
		return protocol.RespondedCommand{}, err
	}
	return protocol.RespondedCommand{Message: &msg}, nil
}
