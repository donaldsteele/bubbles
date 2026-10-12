package core

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/donaldsteele/bubbles/pkg/notifyrules"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// maxBodyRunes is the longest notification text (the web app uses the same).
const maxBodyRunes = 120

// Notification is what the tray shows for one event.
type Notification struct {
	ID        string
	Title     string
	Body      string
	ChannelID string
	MessageID string
	// Critical marks a critical alert; the tray icon turns red until it is
	// acknowledged or resolved (or the user opens bubbles).
	Critical bool
	// Silent is true while notifications are paused: the unread and critical
	// indicators still update, but nothing is shown.
	Silent bool
}

type channelInfo struct {
	kind string
	name string
}

type membershipInfo struct {
	level       string
	minSeverity string
	mutedUntil  *int64
}

// Engine keeps the small slice of server state the notification rules need
// (who I am, my channels and memberships, user names) and turns events into
// notifications. It is fed by Bootstrap snapshots and the event stream.
type Engine struct {
	mu          sync.Mutex
	meID        string
	channels    map[string]channelInfo
	members     map[string]membershipInfo
	users       map[string]string
	unread      map[string]int // notified messages per channel since read
	criticalIDs map[string]struct{}
	pausedUntil int64
}

// NewEngine returns an empty Engine.
func NewEngine() *Engine {
	return &Engine{
		channels:    map[string]channelInfo{},
		members:     map[string]membershipInfo{},
		users:       map[string]string{},
		criticalIDs: map[string]struct{}{},
		unread:      map[string]int{},
	}
}

// Load replaces the engine state with a Bootstrap snapshot. Unread and
// critical indicators are kept.
func (e *Engine) Load(b *protocol.Bootstrap) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.meID = b.Me.ID
	e.channels = make(map[string]channelInfo, len(b.Channels))
	for _, c := range b.Channels {
		name := ""
		if c.Name != nil {
			name = *c.Name
		}
		e.channels[c.ID] = channelInfo{kind: c.Kind, name: name}
	}
	e.members = make(map[string]membershipInfo, len(b.Memberships))
	for _, m := range b.Memberships {
		if m.UserID == b.Me.ID {
			e.members[m.ChannelID] = toMembership(m)
		}
	}
	e.users = make(map[string]string, len(b.Users)+1)
	for _, u := range b.Users {
		e.users[u.ID] = u.DisplayName
	}
	e.users[b.Me.ID] = b.Me.DisplayName
}

func toMembership(m protocol.Membership) membershipInfo {
	mi := membershipInfo{level: m.NotifyLevel, minSeverity: m.AlertMinSeverity}
	if m.MutedUntil != nil {
		if t, err := time.Parse(protocol.TimeLayout, *m.MutedUntil); err == nil {
			ms := t.UnixMilli()
			mi.mutedUntil = &ms
		}
	}
	return mi
}

// SetPausedUntil sets the tray-wide pause (unix ms; 0 clears it).
func (e *Engine) SetPausedUntil(ms int64) {
	e.mu.Lock()
	e.pausedUntil = ms
	e.mu.Unlock()
}

// Indicator is the tray icon state.
type Indicator int

const (
	IndicatorNormal Indicator = iota
	IndicatorUnread
	IndicatorCritical
)

// Indicator returns the icon state: critical beats unread beats normal.
func (e *Engine) Indicator() Indicator {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case len(e.criticalIDs) > 0:
		return IndicatorCritical
	case len(e.unread) > 0:
		return IndicatorUnread
	}
	return IndicatorNormal
}

// MarkSeen clears the indicators (the user opened bubbles).
func (e *Engine) MarkSeen() {
	e.mu.Lock()
	clear(e.unread)
	clear(e.criticalIDs)
	e.mu.Unlock()
}

// Result is the outcome of one event.
type Result struct {
	// Notify is the notification to show, nil for none.
	Notify *Notification
	// Refresh asks for a new Bootstrap: the event referred to state the
	// engine does not have, or the stream says it missed events.
	Refresh bool
	// Changed reports that the indicator may have changed.
	Changed bool
}

// Handle evaluates one event at time now.
func (e *Engine) Handle(ev protocol.Event, now time.Time) Result {
	switch ev.Type {
	case "sdk.resync_required", "sdk.instance_changed":
		return Result{Refresh: true}
	case "membership.updated":
		var m protocol.Membership
		if json.Unmarshal(ev.Data, &m) != nil {
			return Result{}
		}
		e.mu.Lock()
		if m.UserID == e.meID {
			e.members[m.ChannelID] = toMembership(m)
		}
		e.mu.Unlock()
		return Result{}
	case "channel.created", "channel.member_joined":
		return Result{Refresh: true}
	case "message.created":
		var m protocol.Message
		if json.Unmarshal(ev.Data, &m) != nil {
			return Result{}
		}
		return e.message(m, protocol.AlertTransitionCreated, now)
	case "alert.updated":
		var a protocol.AlertUpdatedEvent
		if json.Unmarshal(ev.Data, &a) != nil {
			return Result{}
		}
		return e.message(a.Message, a.Transition, now)
	case "read.updated":
		var r protocol.ReadEvent
		if json.Unmarshal(ev.Data, &r) != nil {
			return Result{}
		}
		return e.read(r)
	}
	return Result{}
}

// read clears the unread mark of a channel the user caught up with (in the
// web app or on another device).
func (e *Engine) read(r protocol.ReadEvent) Result {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r.Unread != 0 {
		return Result{}
	}
	if _, ok := e.unread[r.ChannelID]; !ok {
		return Result{}
	}
	delete(e.unread, r.ChannelID)
	return Result{Changed: true}
}

func (e *Engine) message(m protocol.Message, transition string, now time.Time) Result {
	e.mu.Lock()
	defer e.mu.Unlock()
	if m.Kind == notifyrules.KindAlert && (transition == protocol.AlertTransitionAcked || transition == protocol.AlertTransitionResolved) {
		if _, ok := e.criticalIDs[m.ID]; ok {
			delete(e.criticalIDs, m.ID)
			return Result{Changed: true}
		}
	}
	if m.Deleted {
		return Result{}
	}
	ch, okC := e.channels[m.ChannelID]
	mem, okM := e.members[m.ChannelID]
	if !okC || !okM {
		return Result{Refresh: true}
	}
	in := notifyrules.Input{
		RecipientID:      e.meID,
		AuthorID:         m.Author.ID,
		ChannelKind:      ch.kind,
		NotifyLevel:      mem.level,
		AlertMinSeverity: mem.minSeverity,
		MutedUntil:       mem.mutedUntil,
		Now:              now.UnixMilli(),
		Mentions: notifyrules.Mentions{
			UserIDs: m.Mentions.UserIDs, Channel: m.Mentions.Channel, Here: m.Mentions.Here,
		},
		RecipientOnline: true,
		MessageKind:     m.Kind,
	}
	if m.Kind == notifyrules.KindAlert {
		in.AlertTransition = transition
		if m.Alert != nil {
			in.AlertSeverity = m.Alert.Severity
		}
	}
	if !notifyrules.Decide(in).Notify {
		return Result{}
	}
	n := &Notification{
		ID:        m.ID,
		ChannelID: m.ChannelID,
		MessageID: m.ID,
		Silent:    now.UnixMilli() < e.pausedUntil,
	}
	n.Title, n.Body = e.render(m, ch)
	e.unread[m.ChannelID]++
	if m.Kind == notifyrules.KindAlert && m.Alert != nil && m.Alert.Severity == notifyrules.SeverityCritical {
		n.Critical = true
		e.criticalIDs[m.ID] = struct{}{}
	}
	return Result{Notify: n, Changed: true}
}

// render builds the title and body the same way the web app does.
func (e *Engine) render(m protocol.Message, ch channelInfo) (title, body string) {
	sender := m.Author.DisplayName
	if m.OverrideName != nil && *m.OverrideName != "" {
		sender = *m.OverrideName
	} else if n := e.users[m.Author.ID]; n != "" {
		sender = n
	}
	title = sender
	if ch.name != "" {
		title = sender + " in #" + ch.name
	}
	text := m.Body
	if m.Kind == notifyrules.KindAlert && m.Alert != nil {
		text = m.Alert.Title
	}
	return clean(title, 80), clean(text, maxBodyRunes)
}

// clean flattens whitespace and limits text to max runes (with an ellipsis).
func clean(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-3]) + "..."
}
