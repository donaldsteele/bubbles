// Package notify decides, server-side, who gets a Web Push for a message or an
// alert (plan pipeline steps 6 and 7). In-app, browser and tray notifications
// are decided by the clients with the same pkg/notifyrules; the server only
// owns the case where no window is looking: it applies the rules to every
// channel member and hands the notifiable ones, who are not focused on a
// bubbles window, to the push sender.
package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/internal/push"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/notifyrules"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// QueueSize bounds the events waiting for the dispatcher. The writer
// goroutine never waits for it: when the queue is full an event is dropped
// and counted (a missed push is better than a stalled write path).
const QueueSize = 10000

// Pusher is the Web Push sender (*push.Service).
type Pusher interface {
	// HasSubscriptions reports whether the user has a push subscription.
	HasSubscriptions(ctx context.Context, userID string) bool
	// Send queues a notification for every subscription of the user.
	Send(userID string, n push.Notification)
}

// Settings are the instance settings the dispatcher reads (*settings.Store).
type Settings interface {
	WebPushEnabled() bool
	PushPayload() string
}

// Options configures New.
type Options struct {
	// DB supplies channel members and their notification preferences.
	DB *store.DB
	// Settings, Presence and Pusher are required.
	Settings Settings
	Presence Presence
	Pusher   Pusher
	// Now is the clock of the mute check; nil means time.Now.
	Now func() time.Time
	// Logger receives drops and failures; it never sees message text.
	Logger *slog.Logger
	// QueueSize defaults to the package QueueSize.
	QueueSize int
}

// Dispatcher turns published events into push notifications. It implements
// events.Publisher: install it next to the hub with events.NewMultiPublisher.
type Dispatcher struct {
	db       *store.DB
	settings Settings
	presence Presence
	pusher   Pusher
	now      func() time.Time
	logger   *slog.Logger

	queue   chan events.Event
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	started sync.Once
	closed  atomic.Bool
	dropped atomic.Int64
}

// New returns a stopped Dispatcher; Start runs it. Events published before
// Start wait in the queue.
func New(o Options) *Dispatcher {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.QueueSize <= 0 {
		o.QueueSize = QueueSize
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Dispatcher{
		db: o.DB, settings: o.Settings, presence: o.Presence, pusher: o.Pusher,
		now: o.Now, logger: o.Logger,
		queue: make(chan events.Event, o.QueueSize), ctx: ctx, cancel: cancel,
	}
}

// Start launches the dispatch goroutine. It is idempotent.
func (d *Dispatcher) Start() {
	d.started.Do(func() {
		d.wg.Add(1)
		go d.run()
	})
}

// Close stops the dispatcher, discarding queued events. It is idempotent.
func (d *Dispatcher) Close() {
	if d.closed.Swap(true) {
		return
	}
	d.cancel()
	d.wg.Wait()
}

// Dropped is the number of events discarded because the queue was full.
func (d *Dispatcher) Dropped() int64 { return d.dropped.Load() }

// Publish implements events.Publisher. It never blocks: events that cannot
// notify anyone are ignored, the rest are queued in publish (= seq) order, and
// a full queue drops the event and counts it.
func (d *Dispatcher) Publish(e events.Event) {
	if e.Type != events.TypeMessageCreated && e.Type != events.TypeAlertUpdated {
		return
	}
	if d.closed.Load() {
		return
	}
	select {
	case d.queue <- e:
	default:
		if c := d.dropped.Add(1); c == 1 || c%1000 == 0 {
			d.logger.Warn("notify: dispatch queue full, dropping events", "dropped_total", c)
		}
	}
}

func (d *Dispatcher) run() {
	defer d.wg.Done()
	for {
		select {
		case <-d.ctx.Done():
			return
		case e := <-d.queue:
			d.safeHandle(e)
		}
	}
}

// safeHandle keeps a bug in one event from taking the server down.
func (d *Dispatcher) safeHandle(e events.Event) {
	defer func() {
		if r := recover(); r != nil {
			d.logger.Error("notify: panic while dispatching", "type", e.Type, "seq", e.Seq, "panic", fmt.Sprint(r))
		}
	}()
	d.Handle(d.ctx, e)
}

// event is what Handle extracts from message.created and alert.updated.
type event struct {
	msg        protocol.Message
	transition string
}

// Handle processes one event synchronously. The dispatch goroutine calls it
// for every queued event; tests call it directly.
//
// Only message.created (text and alert messages) and alert.updated notify.
// A new alert message counts as the alert's "created" transition; alert.updated
// carries its own transition (protocol.AlertUpdatedEvent).
func (d *Dispatcher) Handle(ctx context.Context, e events.Event) {
	ev, ok := d.decode(e)
	if !ok || !d.settings.WebPushEnabled() {
		return
	}
	msg := ev.msg
	if msg.Deleted || msg.ChannelID == "" {
		return
	}
	recipients, err := d.recipients(ctx, msg.ChannelID, msg.Author.ID)
	if err != nil {
		if ctx.Err() == nil {
			d.logger.Warn("notify: load recipients", "channel", msg.ChannelID, "err", err)
		}
		return
	}
	if len(recipients) == 0 {
		return
	}
	in := notifyrules.Input{
		AuthorID:        msg.Author.ID,
		Now:             d.now().UnixMilli(),
		Mentions:        notifyrules.Mentions{UserIDs: msg.Mentions.UserIDs, Channel: msg.Mentions.Channel, Here: msg.Mentions.Here},
		MessageKind:     msg.Kind,
		AlertTransition: ev.transition,
	}
	critical := false
	if msg.Kind == protocol.MessageAlert && msg.Alert != nil {
		in.AlertSeverity = msg.Alert.Severity
		critical = msg.Alert.Severity == protocol.SeverityCritical
	}

	var note *push.Notification // built once, on the first recipient to push
	for _, r := range recipients {
		in.RecipientID = r.userID
		in.ChannelKind = r.channelKind
		in.NotifyLevel = r.notifyLevel
		in.AlertMinSeverity = r.alertMinSeverity
		in.MutedUntil = r.mutedUntil
		in.RecipientOnline = d.presence.Online(r.userID)
		if !notifyrules.Decide(in).Notify {
			continue
		}
		if d.presence.UserFocusedRecently(r.userID, FocusWindow) {
			continue
		}
		if !d.pusher.HasSubscriptions(ctx, r.userID) {
			continue
		}
		if note == nil {
			n := d.notification(ctx, msg, r, critical)
			note = &n
		}
		d.pusher.Send(r.userID, *note)
	}
}

// decode extracts the message and alert transition from e. ok is false for
// events that never notify: other types, system messages, malformed payloads.
func (d *Dispatcher) decode(e events.Event) (event, bool) {
	switch e.Type {
	case events.TypeMessageCreated:
		var m protocol.Message
		if err := json.Unmarshal(e.Payload, &m); err != nil {
			d.logger.Warn("notify: undecodable message.created payload", "seq", e.Seq)
			return event{}, false
		}
		if m.Kind == protocol.MessageSystem {
			return event{}, false
		}
		ev := event{msg: m}
		if m.Kind == protocol.MessageAlert {
			ev.transition = protocol.AlertTransitionCreated
		}
		return ev, true
	case events.TypeAlertUpdated:
		var u protocol.AlertUpdatedEvent
		if err := json.Unmarshal(e.Payload, &u); err != nil {
			d.logger.Warn("notify: undecodable alert.updated payload", "seq", e.Seq)
			return event{}, false
		}
		u.Message.Kind = protocol.MessageAlert
		return event{msg: u.Message, transition: u.Transition}, true
	}
	return event{}, false
}

// recipient is a channel member with the preferences the rules need.
type recipient struct {
	userID           string
	channelKind      string
	channelName      string
	notifyLevel      string
	alertMinSeverity string
	mutedUntil       *int64
}

// recipients lists the human, active members of the channel except the
// author. Apps (bots) never receive notifications.
func (d *Dispatcher) recipients(ctx context.Context, channelID, authorID string) ([]recipient, error) {
	rows, err := d.db.Read().QueryContext(ctx, `
		SELECT m.user_id, COALESCE(m.notify_level, 'mentions'), m.alert_min_severity, m.muted_until,
		       c.kind, COALESCE(c.name, '')
		FROM memberships m
		JOIN channels c ON c.id = m.channel_id
		JOIN users u ON u.id = m.user_id
		WHERE m.channel_id = ? AND m.user_id <> ? AND u.kind = 'human' AND u.deactivated_at IS NULL
		ORDER BY m.user_id`, channelID, authorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []recipient
	for rows.Next() {
		var r recipient
		var muted sql.NullInt64
		if err := rows.Scan(&r.userID, &r.notifyLevel, &r.alertMinSeverity, &muted, &r.channelKind, &r.channelName); err != nil {
			return nil, err
		}
		if muted.Valid {
			v := muted.Int64
			r.mutedUntil = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// notification builds the push content. The message text is looked up only
// when the admin chose full payloads, so a minimal-payload instance never
// even reads it.
func (d *Dispatcher) notification(ctx context.Context, msg protocol.Message, r recipient, critical bool) push.Notification {
	n := push.Notification{
		ChannelID:   msg.ChannelID,
		ChannelName: r.channelName,
		ChannelKind: r.channelKind,
		MessageID:   msg.ID,
		SenderName:  senderName(msg),
		Critical:    critical,
	}
	if d.settings.PushPayload() == settings.PushFull {
		n.Body = d.body(ctx, msg)
	}
	return n
}

// body is the plain text of msg: the alert title for alerts, else the
// message's searchable plain text, else its Markdown source.
func (d *Dispatcher) body(ctx context.Context, msg protocol.Message) string {
	if msg.Kind == protocol.MessageAlert && msg.Alert != nil && msg.Alert.Title != "" {
		return msg.Alert.Title
	}
	var plain string
	err := d.db.Read().QueryRowContext(ctx, `SELECT body_plain FROM messages WHERE id = ?`, msg.ID).Scan(&plain)
	if err == nil && plain != "" {
		return plain
	}
	return msg.Body
}

// senderName is the display name the push shows: a webhook's override name,
// else the author's display name, else the username.
func senderName(msg protocol.Message) string {
	if msg.OverrideName != nil && *msg.OverrideName != "" {
		return *msg.OverrideName
	}
	if msg.Author.DisplayName != "" {
		return msg.Author.DisplayName
	}
	return msg.Author.Username
}
