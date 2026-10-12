package events

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Presence timing (plan pipeline step 4).
const (
	// ActivityWindow: a user is online while a connection of theirs was
	// active within it, idle while connected without such activity.
	ActivityWindow = 5 * time.Minute
	// OfflineGrace: a user with no connections turns offline only after it,
	// so a reconnect does not flap their presence.
	OfflineGrace = 30 * time.Second
	// PresenceSweepInterval is how often the app calls SweepPresence.
	PresenceSweepInterval = 5 * time.Second
	// PresenceCoalesce is the least gap between two presence.changed events of
	// one user. A change inside the gap is held and published by the first
	// SweepPresence after the gap, carrying the state current at that point.
	PresenceCoalesce = 5 * time.Second
	// focusMemory bounds how long a hidden or closed connection's last
	// focus is remembered for UserFocusedRecently.
	focusMemory = 10 * time.Minute
)

// presenceSlot is the coalescing state of one user: when presence.changed was
// last published for them, and whether a later change is waiting for the gap
// to pass.
type presenceSlot struct {
	at      time.Time
	pending bool
}

// SetPresence records a client presence report: protocol.PresenceActive
// (user activity now) or protocol.PresenceIdle.
func (s *Subscription) SetPresence(state string) {
	h := s.hub
	h.mu.Lock()
	if s.closed.Load() {
		h.mu.Unlock()
		return
	}
	now := h.now()
	if state == protocol.PresenceActive {
		s.idle, s.lastActive = false, now
	} else {
		s.idle = true
	}
	evs := h.updatePresenceLocked(s.viewer.UserID, now)
	h.mu.Unlock()
	h.publishAll(evs)
}

// Focus records whether the connection's window is visible and focused
// (channelID is informational). Becoming visible counts as activity.
func (s *Subscription) Focus(visible bool) {
	h := s.hub
	h.mu.Lock()
	if s.closed.Load() {
		h.mu.Unlock()
		return
	}
	now := h.now()
	uid := s.viewer.UserID
	if s.visible && !visible {
		h.hiddenAt[uid] = now
	}
	s.visible = visible
	var evs []Event
	if visible {
		s.idle, s.lastActive = false, now
		evs = h.updatePresenceLocked(uid, now)
	}
	h.mu.Unlock()
	h.publishAll(evs)
}

// UserFocusedRecently reports whether userID has a connection whose window
// is visible now, or had one within the last within (Web Push suppression).
func (h *Hub) UserFocusedRecently(userID string, within time.Duration) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.byUser[userID] {
		if s.visible {
			return true
		}
	}
	t, ok := h.hiddenAt[userID]
	return ok && h.now().Sub(t) <= within
}

// SweepPresence re-evaluates every connected or recently disconnected user
// (online -> idle after ActivityWindow, -> offline after OfflineGrace), flushes
// presence changes held by PresenceCoalesce, and forgets expired typing,
// coalescing and focus records. The app calls it every PresenceSweepInterval.
func (h *Hub) SweepPresence() {
	h.mu.Lock()
	now := h.now()
	users := map[string]bool{}
	for u := range h.byUser {
		users[u] = true
	}
	for u := range h.goneAt {
		users[u] = true
	}
	for u, sl := range h.presenceSlots {
		if sl.pending {
			users[u] = true
		}
	}
	ids := make([]string, 0, len(users))
	for u := range users {
		ids = append(ids, u)
	}
	sort.Strings(ids)
	var evs []Event
	for _, u := range ids {
		evs = append(evs, h.updatePresenceLocked(u, now)...)
	}
	h.pruneTypingLocked(now)
	for u, sl := range h.presenceSlots {
		if !sl.pending && now.Sub(sl.at) >= PresenceCoalesce {
			delete(h.presenceSlots, u)
		}
	}
	for u, t := range h.hiddenAt {
		if now.Sub(t) > focusMemory {
			delete(h.hiddenAt, u)
		}
	}
	h.mu.Unlock()
	h.publishAll(evs)
}

// PresenceSnapshot returns presence.changed frames (seq 0) for every
// non-offline user v may see, sorted by user id. A new connection sends it
// after its replay; users not listed are offline.
func (h *Hub) PresenceSnapshot(v Viewer) []Frame {
	h.mu.Lock()
	now := h.now()
	states := make(map[string]string, len(h.presence))
	for u, st := range h.presence {
		states[u] = st
	}
	h.mu.Unlock()
	users := make([]string, 0, len(states))
	for u := range states {
		users = append(users, u)
	}
	sort.Strings(users)
	var out []Frame
	for _, u := range users {
		e := presenceEvent(u, states[u], now)
		if !Visible(v, e, h.cache) {
			continue
		}
		if data, err := EncodeFrame(e); err == nil {
			out = append(out, Frame{Data: data})
		}
	}
	return out
}

// presenceOfLocked computes userID's presence now. Callers hold h.mu.
func (h *Hub) presenceOfLocked(userID string, now time.Time) string {
	if subs := h.byUser[userID]; len(subs) > 0 {
		for s := range subs {
			if !s.idle && now.Sub(s.lastActive) < ActivityWindow {
				return protocol.PresenceOnline
			}
		}
		return protocol.PresenceIdle
	}
	if gone, ok := h.goneAt[userID]; ok && now.Sub(gone) < OfflineGrace {
		if st, ok := h.presence[userID]; ok {
			return st
		}
	}
	return protocol.PresenceOffline
}

// updatePresenceLocked recomputes userID's presence and returns the
// presence.changed event to publish when it changed. A change less than
// PresenceCoalesce after the user's last publication is held as pending
// instead; SweepPresence publishes it once the gap has passed, unless the state
// has meanwhile returned to the published one. Callers hold h.mu.
func (h *Hub) updatePresenceLocked(userID string, now time.Time) []Event {
	if userID == "" {
		return nil
	}
	st := h.presenceOfLocked(userID, now)
	prev, ok := h.presence[userID]
	if !ok {
		prev = protocol.PresenceOffline
	}
	if st == protocol.PresenceOffline {
		delete(h.goneAt, userID)
	}
	if st == prev {
		if sl, ok := h.presenceSlots[userID]; ok && sl.pending {
			sl.pending = false
			h.presenceSlots[userID] = sl
		}
		return nil
	}
	sl := h.presenceSlots[userID]
	if now.Sub(sl.at) < PresenceCoalesce {
		sl.pending = true
		h.presenceSlots[userID] = sl
		return nil
	}
	h.presenceSlots[userID] = presenceSlot{at: now}
	if st == protocol.PresenceOffline {
		delete(h.presence, userID)
	} else {
		h.presence[userID] = st
	}
	return []Event{presenceEvent(userID, st, now)}
}

func presenceEvent(userID, state string, now time.Time) Event {
	payload, _ := json.Marshal(protocol.PresenceEvent{UserID: userID, State: state})
	return Event{Type: TypePresenceChanged, UserID: userID, Payload: payload, CreatedAt: now.UnixMilli()}
}

// Online reports whether userID is online right now (a connection with
// activity within ActivityWindow; idle and offline users are not). The
// notification rules use it for @here.
func (h *Hub) Online(userID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.presenceOfLocked(userID, h.now()) == protocol.PresenceOnline
}
