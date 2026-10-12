package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// QueueSize is the per-connection send queue length. A connection whose
// queue is full when an event arrives is closed with CloseSlowConsumer; the
// client resumes from its last seq.
const QueueSize = 256

// HubOptions configures NewHub.
type HubOptions struct {
	// DB is the database whose appended events the hub publishes; its read
	// pool backs the membership cache. Required.
	DB *store.DB
	// Now is the presence, typing and focus clock; nil means time.Now.
	Now func() time.Time
	// Logger receives membership load failures; nil discards them.
	Logger *slog.Logger
}

// Frame is one encoded server-to-client event frame (a protocol.Event).
// Seq is 0 for ephemeral frames.
type Frame struct {
	Seq  int64
	Data []byte
}

// Hub is the in-process realtime fan-out. Events reach it through Publish:
// persisted events from the store writer after commit (see SetPublisher),
// ephemeral ones (typing, presence) from the hub itself. A single dispatcher
// goroutine delivers them in publish order to the subscriptions allowed to
// see them (Visible), indexed by user and, through the membership cache, by
// channel. Publish never blocks.
type Hub struct {
	now    func() time.Time
	logger *slog.Logger
	cache  *MembershipCache
	cancel context.CancelFunc

	inMu    sync.Mutex
	inbox   []Event
	stopped bool
	wake    chan struct{}
	done    chan struct{}
	stop    chan struct{}
	once    sync.Once

	mu            sync.Mutex
	subs          map[*Subscription]struct{}
	byUser        map[string]map[*Subscription]struct{}
	presence      map[string]string       // published state of every non-offline user
	goneAt        map[string]time.Time    // when a user's last connection ended
	typingAt      map[string]time.Time    // last relayed typing per user+channel
	hiddenAt      map[string]time.Time    // when a visible connection of a user last went away
	presenceSlots map[string]presenceSlot // presence.changed coalescing per user
	// typingPrunedAt is when typingAt was last swept of stale records.
	typingPrunedAt time.Time
}

// NewHub starts a hub and registers it as the post-commit publisher of
// o.DB. Close stops it.
func NewHub(o HubOptions) *Hub {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &Hub{
		now:           o.Now,
		logger:        o.Logger,
		cache:         NewMembershipCache(ctx, o.DB.Read(), o.Logger),
		cancel:        cancel,
		wake:          make(chan struct{}, 1),
		done:          make(chan struct{}),
		stop:          make(chan struct{}),
		subs:          map[*Subscription]struct{}{},
		byUser:        map[string]map[*Subscription]struct{}{},
		presence:      map[string]string{},
		goneAt:        map[string]time.Time{},
		typingAt:      map[string]time.Time{},
		hiddenAt:      map[string]time.Time{},
		presenceSlots: map[string]presenceSlot{},
	}
	go h.loop()
	SetPublisher(o.DB, h)
	return h
}

// Close stops the dispatcher; later publishes are dropped. Subscriptions
// are left to their owners (see the gateway's CloseAll). It is idempotent.
func (h *Hub) Close() {
	h.once.Do(func() {
		h.inMu.Lock()
		h.stopped = true
		h.inbox = nil
		h.inMu.Unlock()
		close(h.stop)
		<-h.done
		h.cancel()
	})
}

// Publish queues e for delivery. It never blocks: it is called on the store
// writer goroutine after each commit, in seq order.
func (h *Hub) Publish(e Event) {
	h.inMu.Lock()
	if h.stopped {
		h.inMu.Unlock()
		return
	}
	h.inbox = append(h.inbox, e)
	h.inMu.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// Membership exposes the hub's live membership cache. It is the entry point the
// reset lifecycle uses to call InvalidateAll once the content is wiped.
func (h *Hub) Membership() *MembershipCache { return h.cache }

// loop is the dispatcher goroutine.
func (h *Hub) loop() {
	defer close(h.done)
	for {
		select {
		case <-h.stop:
			return
		case <-h.wake:
		}
		for {
			h.inMu.Lock()
			batch := h.inbox
			h.inbox = nil
			h.inMu.Unlock()
			if len(batch) == 0 {
				break
			}
			for _, e := range batch {
				select {
				case <-h.stop:
					return
				default:
				}
				h.dispatch(e)
			}
		}
	}
}

// target is a subscription with the viewer it had when an event was routed.
type target struct {
	sub    *Subscription
	viewer Viewer
}

func (h *Hub) dispatch(e Event) {
	if e.Type == TypeTyping && !h.cache.IsMember(e.ChannelID, e.UserID) {
		return
	}
	h.applyEffects(e)
	data, err := EncodeFrame(e)
	if err != nil {
		h.logger.Warn("realtime: encode event", "type", e.Type, "seq", e.Seq, "err", err)
		return
	}
	f := Frame{Seq: e.Seq, Data: data}
	for _, t := range h.candidates(e) {
		if Visible(t.viewer, e, h.cache) {
			t.sub.deliver(f)
		}
	}
	h.closeEffects(e)
}

// userScoped lists the types routed by the user column alone.
func userScoped(typ string) bool {
	switch typ {
	case TypeReadUpdated, TypeMembershipUpdated, TypeSessionRevoked, TypeEphemeralMessage, TypeCommandInvoked:
		return true
	}
	return false
}

// lifecycle lists the channel types that may reach non-members.
func lifecycle(typ string) bool {
	switch typ {
	case TypeChannelCreated, TypeChannelUpdated, TypeChannelArchived, TypeChannelUnarchived:
		return true
	}
	return false
}

// candidates narrows the subscriptions an event may reach using the user
// index and the channel's member list; Visible makes the final decision.
func (h *Hub) candidates(e Event) []target {
	var members []string
	channelScoped := e.ChannelID != "" && !userScoped(e.Type) && !lifecycle(e.Type) &&
		e.Type != TypeUserUpdated && e.Type != TypePresenceChanged && e.Type != TypeSettingsUpdated
	if channelScoped {
		members = h.cache.Members(e.ChannelID)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []target
	addUser := func(id string) {
		for s := range h.byUser[id] {
			out = append(out, target{s, s.viewer})
		}
	}
	switch {
	case userScoped(e.Type):
		addUser(e.UserID)
	case channelScoped && len(members) < len(h.subs):
		for _, m := range members {
			addUser(m)
		}
		if e.Type == TypeChannelMemberLeft {
			if u := payloadString(e.Payload, "user_id"); !slices.Contains(members, u) {
				addUser(u)
			}
		}
	default:
		for s := range h.subs {
			out = append(out, target{s, s.viewer})
		}
	}
	return out
}

// applyEffects keeps the membership cache and the viewers current before
// an event is routed.
func (h *Hub) applyEffects(e Event) {
	switch e.Type {
	case TypeChannelMemberJoined, TypeChannelMemberLeft:
		h.cache.InvalidateChannel(e.ChannelID)
		h.cache.InvalidateUser(payloadString(e.Payload, "user_id"))
	case TypeChannelCreated:
		h.cache.InvalidateChannel(e.ChannelID)
		var p struct {
			Memberships []struct {
				UserID string `json:"user_id"`
			} `json:"memberships"`
		}
		if json.Unmarshal(e.Payload, &p) == nil {
			for _, m := range p.Memberships {
				h.cache.InvalidateUser(m.UserID)
			}
		}
	case TypeMembershipUpdated:
		h.cache.InvalidateUser(e.UserID)
		h.cache.InvalidateChannel(payloadString(e.Payload, "channel_id"))
	case TypeUserUpdated:
		if role := payloadString(e.Payload, "role"); role != "" {
			h.mu.Lock()
			for s := range h.byUser[subjectOf(e)] {
				s.viewer.Role = role
			}
			h.mu.Unlock()
		}
	}
}

// closeEffects closes the sockets an event ends: revoked sessions, revoked or
// expired app tokens (a token_id closes one token's connections, an app_id
// alone closes every token's connections of that app) and deactivated users.
func (h *Hub) closeEffects(e Event) {
	switch e.Type {
	case TypeSessionRevoked:
		var p struct {
			SessionID string `json:"session_id"`
			All       bool   `json:"all"`
			Except    string `json:"except_session_id"`
			TokenID   string `json:"token_id"`
			AppID     string `json:"app_id"`
		}
		if json.Unmarshal(e.Payload, &p) != nil {
			return
		}
		h.closeUser(e.UserID, func(s *Subscription) bool {
			if s.viewer.TokenID != "" {
				if p.TokenID != "" {
					return p.TokenID == s.viewer.TokenID
				}
				return p.AppID != "" && p.AppID == s.viewer.AppID
			}
			if s.sessionID == "" {
				return false
			}
			if p.SessionID != "" && s.sessionID == p.SessionID {
				return true
			}
			return p.All && s.sessionID != p.Except
		})
	case TypeUserUpdated:
		var p struct {
			Deactivated bool `json:"deactivated"`
		}
		if json.Unmarshal(e.Payload, &p) == nil && p.Deactivated {
			h.closeUser(subjectOf(e), func(*Subscription) bool { return true })
		}
	}
}

func (h *Hub) closeUser(userID string, match func(*Subscription) bool) {
	if userID == "" {
		return
	}
	h.mu.Lock()
	var victims []*Subscription
	for s := range h.byUser[userID] {
		if match(s) {
			victims = append(victims, s)
		}
	}
	h.mu.Unlock()
	for _, s := range victims {
		s.Close(CloseSessionRevoked, ReasonSessionRevoked)
	}
}

// Subscribers returns the number of live subscriptions of userID, or of
// everyone when userID is "".
func (h *Hub) Subscribers(userID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if userID == "" {
		return len(h.subs)
	}
	return len(h.byUser[userID])
}

// publishAll publishes ephemeral events built under h.mu.
func (h *Hub) publishAll(evs []Event) {
	for _, e := range evs {
		h.Publish(e)
	}
}

// EncodeFrame renders e as its wire envelope (protocol.Event). Live
// delivery and replay share it.
func EncodeFrame(e Event) ([]byte, error) {
	data := e.Payload
	if len(data) == 0 {
		data = json.RawMessage("{}")
	}
	return json.Marshal(protocol.Event{
		Seq: e.Seq, Type: e.Type, ChannelID: e.ChannelID,
		TS: protocol.FormatTime(e.CreatedAt), Data: data,
	})
}

// Subscription is one realtime connection registered with the hub.
type Subscription struct {
	hub       *Hub
	sessionID string
	frames    chan Frame
	closeFn   func(code int, reason string)
	closed    atomic.Bool

	// Guarded by hub.mu.
	viewer     Viewer
	idle       bool
	lastActive time.Time
	visible    bool
	// expiry closes a bearer connection when its token expires; nil when the
	// token never expires.
	expiry *time.Timer
}

// Subscribe registers a connection of v (cookie session sessionID, "" for
// bearer principals). From the moment it returns, every event published
// and visible to v is queued on Frames. closeFn is called at most once, from
// the dispatcher, when the hub ends the subscription (slow consumer, revoked
// session or token, deactivated user, CloseAll); it must not block. A bearer
// subscription whose token expires is closed from a timer at that instant,
// so closeFn may also run from that timer's goroutine.
func (h *Hub) Subscribe(v Viewer, sessionID string, closeFn func(code int, reason string)) *Subscription {
	s := &Subscription{
		hub: h, sessionID: sessionID, frames: make(chan Frame, QueueSize),
		closeFn: closeFn, viewer: v,
	}
	h.mu.Lock()
	now := h.now()
	s.lastActive = now
	h.subs[s] = struct{}{}
	if v.TokenExpiresAt > 0 {
		d := max(time.UnixMilli(v.TokenExpiresAt).Sub(now), 0)
		s.expiry = time.AfterFunc(d, func() { s.Close(CloseSessionRevoked, ReasonSessionRevoked) })
	}
	set := h.byUser[v.UserID]
	if set == nil {
		set = map[*Subscription]struct{}{}
		h.byUser[v.UserID] = set
	}
	set[s] = struct{}{}
	delete(h.goneAt, v.UserID)
	evs := h.updatePresenceLocked(v.UserID, now)
	h.mu.Unlock()
	h.publishAll(evs)
	return s
}

// Frames is the subscription's send queue (QueueSize). It is never closed.
func (s *Subscription) Frames() <-chan Frame { return s.frames }

// Viewer returns the subscription's current viewer.
func (s *Subscription) Viewer() Viewer {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	v := s.viewer
	v.ChannelIDs = slices.Clone(v.ChannelIDs)
	return v
}

// Close ends the subscription and calls its closeFn with code and reason.
// Only the first Close or Leave has an effect.
func (s *Subscription) Close(code int, reason string) {
	if s.detach() && s.closeFn != nil {
		s.closeFn(code, reason)
	}
}

// Leave ends the subscription without calling closeFn (the connection is
// already going away).
func (s *Subscription) Leave() { s.detach() }

// detach removes s from the hub once; it reports whether this call did.
func (s *Subscription) detach() bool {
	if !s.closed.CompareAndSwap(false, true) {
		return false
	}
	h := s.hub
	h.mu.Lock()
	now := h.now()
	uid := s.viewer.UserID
	if s.expiry != nil {
		s.expiry.Stop()
	}
	delete(h.subs, s)
	if set := h.byUser[uid]; set != nil {
		delete(set, s)
		if len(set) == 0 {
			delete(h.byUser, uid)
			h.goneAt[uid] = now
		}
	}
	if s.visible {
		h.hiddenAt[uid] = now
	}
	evs := h.updatePresenceLocked(uid, now)
	h.mu.Unlock()
	h.publishAll(evs)
	return true
}

// deliver queues f without blocking; a full queue closes the subscription
// with CloseSlowConsumer so the client never sees a gap, only a close.
func (s *Subscription) deliver(f Frame) {
	if s.closed.Load() {
		return
	}
	select {
	case s.frames <- f:
	default:
		s.Close(CloseSlowConsumer, ReasonSlowConsumer)
	}
}
