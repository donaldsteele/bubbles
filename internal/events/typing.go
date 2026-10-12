package events

import (
	"encoding/json"
	"time"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// TypingInterval is the minimum gap between two relayed typing signals of
// one user in one channel; extra signals are dropped. Clients expire a
// typing indicator 6 s after the last signal.
const TypingInterval = 3 * time.Second

// maxTypingEntries caps the typing throttle map. A signal that would need a
// new entry beyond the cap is dropped until stale entries are pruned.
const maxTypingEntries = 10000

// Typing relays a typing signal for channelID to the channel's other
// members as an ephemeral (seq 0) typing event, at most once per
// TypingInterval per user and channel. The sender must be a member of the
// channel: unknown, foreign or disallowed channels are dropped silently before
// any throttle state is touched or anything is published. Typing counts as
// activity.
func (s *Subscription) Typing(channelID string) {
	h := s.hub
	h.mu.Lock()
	uid, allowed := s.viewer.UserID, s.viewer.allows(channelID)
	h.mu.Unlock()
	if channelID == "" || !allowed || !h.cache.IsMember(channelID, uid) {
		return
	}

	h.mu.Lock()
	if s.closed.Load() {
		h.mu.Unlock()
		return
	}
	now := h.now()
	key := uid + "\x00" + channelID
	if last, ok := h.typingAt[key]; ok && now.Sub(last) < TypingInterval {
		h.mu.Unlock()
		return
	}
	if now.Sub(h.typingPrunedAt) >= TypingInterval || len(h.typingAt) >= maxTypingEntries {
		h.pruneTypingLocked(now)
	}
	if _, ok := h.typingAt[key]; !ok && len(h.typingAt) >= maxTypingEntries {
		h.mu.Unlock()
		return
	}
	h.typingAt[key] = now
	s.idle, s.lastActive = false, now
	evs := h.updatePresenceLocked(uid, now)
	h.mu.Unlock()
	h.publishAll(evs)

	payload, _ := json.Marshal(protocol.TypingEvent{ChannelID: channelID, UserID: uid})
	h.Publish(Event{Type: TypeTyping, ChannelID: channelID, UserID: uid, Payload: payload, CreatedAt: now.UnixMilli()})
}

// pruneTypingLocked forgets throttle records older than TypingInterval: such
// a record no longer throttles anything. Callers hold h.mu.
func (h *Hub) pruneTypingLocked(now time.Time) {
	for k, t := range h.typingAt {
		if now.Sub(t) >= TypingInterval {
			delete(h.typingAt, k)
		}
	}
	h.typingPrunedAt = now
}
