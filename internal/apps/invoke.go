package apps

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"

	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// InvocationTTL is how long an app may answer an invocation.
const InvocationTTL = 15 * time.Minute

// Realtime is the part of the events hub the command service needs: it
// publishes ephemeral events (seq 0, never stored) and knows which accounts
// have a live connection.
type Realtime interface {
	Publish(e events.Event)
	Subscribers(userID string) int
}

// invocation is one app command run waiting for its answer. It lives in
// memory only and dies with the process.
type invocation struct {
	appID        string
	appName      string
	botID        string
	invokerID    string
	channelID    string
	threadRootID *string
	expires      int64 // unix ms
}

// Pending invocation caps: an app may have MaxPendingPerApp invocations
// awaiting an answer, and one invoker MaxPendingPerInvoker across all apps.
const (
	MaxPendingPerApp     = 100
	MaxPendingPerInvoker = 20
	// sweepBudget bounds the expired invocations one Invoke removes, so the
	// cost of expiry is spread over calls instead of paid at once.
	sweepBudget = 64
	// minRetryAfter is the Retry-After hint when a cap is hit.
	minRetryAfter = time.Second
)

// PendingLimitError reports that the app or the invoker already has the
// maximum of invocations awaiting an answer. The REST layer answers 429 with
// Retry-After set from RetryAfter.
type PendingLimitError struct {
	RetryAfter time.Duration
}

func (e *PendingLimitError) Error() string { return "too many pending command invocations" }

// pendingRef is one entry of the expiry queue. It is counted against its app
// and invoker until it is popped, whether or not the invocation was looked up
// or removed from the map in the meantime.
type pendingRef struct {
	id        string
	appID     string
	invokerID string
	expires   int64 // unix ms
}

// reserveLocked records inv under id unless a pending cap is reached. It sweeps
// expired entries first: a budgeted sweep on every call, and a full one only
// when a cap looks reached, so stale entries never cause a false refusal. Each
// entry is popped once, so the cost is amortized constant. Caller holds invMu.
func (s *Service) reserveLocked(now int64, id string, inv *invocation) error {
	s.sweepLocked(now, sweepBudget)
	if s.invByApp[inv.appID] >= MaxPendingPerApp || s.invByInvoker[inv.invokerID] >= MaxPendingPerInvoker {
		s.sweepLocked(now, -1)
	}
	if s.invByApp[inv.appID] >= MaxPendingPerApp || s.invByInvoker[inv.invokerID] >= MaxPendingPerInvoker {
		return &PendingLimitError{RetryAfter: s.retryAfterLocked(now)}
	}
	s.invocations[id] = inv
	s.invByApp[inv.appID]++
	s.invByInvoker[inv.invokerID]++
	s.invQueue = append(s.invQueue, pendingRef{id: id, appID: inv.appID, invokerID: inv.invokerID, expires: inv.expires})
	return nil
}

// sweepLocked pops up to budget expired entries from the front of the expiry
// queue, removing each invocation and its pending counts; a negative budget
// pops all of them. Caller holds invMu.
func (s *Service) sweepLocked(now int64, budget int) {
	for n := 0; (budget < 0 || n < budget) && len(s.invQueue) > 0 && s.invQueue[0].expires <= now; n++ {
		ref := s.invQueue[0]
		s.invQueue[0] = pendingRef{}
		s.invQueue = s.invQueue[1:]
		delete(s.invocations, ref.id)
		decCount(s.invByApp, ref.appID)
		decCount(s.invByInvoker, ref.invokerID)
	}
	if len(s.invQueue) == 0 {
		s.invQueue = nil
	}
}

func decCount(m map[string]int, key string) {
	if m[key] <= 1 {
		delete(m, key)
		return
	}
	m[key]--
}

// retryAfterLocked is a Retry-After hint after a full sweep: the time until the
// oldest live invocation expires, since the cap frees no earlier than that.
// Caller holds invMu.
func (s *Service) retryAfterLocked(now int64) time.Duration {
	if len(s.invQueue) == 0 {
		return minRetryAfter
	}
	return max(time.Duration(s.invQueue[0].expires-now)*time.Millisecond, minRetryAfter)
}

// Invoke runs a slash command typed by a person in a channel. Built-ins run
// here and their private notice, if any, goes to the invoker. For an app
// command it tells the app's connections and returns the invocation id; an
// app with no live connection is ErrAppOffline, with an ephemeral notice to
// the invoker.
func (s *Service) Invoke(ctx context.Context, actor chat.Actor, req protocol.InvokeCommandRequest) (protocol.InvokedCommand, error) {
	name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(req.Command), "/"))
	switch {
	case req.ChannelID == "":
		return protocol.InvokedCommand{}, invalid("channel_id", "channel_id is required")
	case !commandName.MatchString(name):
		return protocol.InvokedCommand{}, invalid("command", "command must be a name of lowercase letters, digits or '-'")
	case utf8.RuneCountInString(req.Args) > protocol.MaxCommandArgsChars || !validText(req.Args):
		return protocol.InvokedCommand{}, invalid("args", fmt.Sprintf("args must be valid text of at most %d characters", protocol.MaxCommandArgsChars))
	}
	thread := ""
	if req.ThreadRootID != nil {
		thread = *req.ThreadRootID
	}
	if s.rt == nil {
		return protocol.InvokedCommand{}, errors.New("commands: no realtime hub")
	}

	if chat.IsBuiltin(name) {
		if name != protocol.CommandLeave && name != protocol.CommandMute {
			if err := s.chat.CheckPost(ctx, actor, req.ChannelID, thread); err != nil {
				return protocol.InvokedCommand{}, err
			}
		}
		catalog, err := s.ListCommands(ctx)
		if err != nil {
			return protocol.InvokedCommand{}, err
		}
		res, err := s.chat.RunBuiltin(ctx, actor, chat.BuiltinInput{
			Command: name, Args: req.Args, ChannelID: req.ChannelID, ThreadRootID: thread, Catalog: catalog,
		})
		if err != nil {
			return protocol.InvokedCommand{}, err
		}
		if res.Notice != "" {
			s.notice(actor.ID, req.ChannelID, req.ThreadRootID, "", nil, res.Notice)
		}
		return protocol.InvokedCommand{Builtin: true}, nil
	}

	if err := s.chat.CheckPost(ctx, actor, req.ChannelID, thread); err != nil {
		return protocol.InvokedCommand{}, err
	}
	db := s.db.Read()
	var appID, appName, botID string
	err := db.QueryRowContext(ctx, `SELECT a.id, a.name, a.user_id FROM commands c JOIN apps a ON a.id = c.app_id
		WHERE c.name = ? AND a.disabled_at IS NULL`, name).Scan(&appID, &appName, &botID)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.InvokedCommand{}, notFound()
	}
	if err != nil {
		return protocol.InvokedCommand{}, fmt.Errorf("find command: %w", err)
	}
	var one int
	err = db.QueryRowContext(ctx, `SELECT 1 FROM memberships WHERE channel_id = ? AND user_id = ?`, req.ChannelID, botID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		s.notice(actor.ID, req.ChannelID, req.ThreadRootID, "", nil, appName+" is not in this channel")
		return protocol.InvokedCommand{}, conflict(appName + " is not a member of this channel")
	}
	if err != nil {
		return protocol.InvokedCommand{}, fmt.Errorf("check app membership: %w", err)
	}
	if s.rt.Subscribers(botID) == 0 {
		s.notice(actor.ID, req.ChannelID, req.ThreadRootID, "", nil, appName+" is offline")
		return protocol.InvokedCommand{}, ErrAppOffline
	}
	var inv protocol.CommandInvoker
	err = db.QueryRowContext(ctx, `SELECT id, username, display_name FROM users WHERE id = ?`, actor.ID).Scan(&inv.ID, &inv.Username, &inv.DisplayName)
	if err != nil {
		return protocol.InvokedCommand{}, fmt.Errorf("load invoker: %w", err)
	}

	now := s.now().UnixMilli()
	id := ulid.Make().String()
	pendingInv := &invocation{
		appID: appID, appName: appName, botID: botID, invokerID: actor.ID, channelID: req.ChannelID,
		threadRootID: req.ThreadRootID, expires: now + InvocationTTL.Milliseconds(),
	}
	s.invMu.Lock()
	err = s.reserveLocked(now, id, pendingInv)
	s.invMu.Unlock()
	if err != nil {
		return protocol.InvokedCommand{}, err
	}

	payload, err := json.Marshal(protocol.CommandInvoked{
		InvocationID: id, Command: name, Args: req.Args, ChannelID: req.ChannelID,
		ThreadRootID: req.ThreadRootID, User: inv,
	})
	if err != nil {
		return protocol.InvokedCommand{}, fmt.Errorf("encode command.invoked: %w", err)
	}
	s.rt.Publish(events.Event{Type: events.TypeCommandInvoked, UserID: botID, Payload: payload, CreatedAt: now})
	return protocol.InvokedCommand{InvocationID: id}, nil
}
