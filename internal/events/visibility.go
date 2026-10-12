package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Viewer is the identity events are delivered to: the account behind one
// realtime connection.
type Viewer struct {
	UserID string
	// Role is protocol.RoleAdmin, RoleMember or RoleGuest.
	Role string
	// Kind is protocol.KindHuman or KindApp.
	Kind string
	// ChannelIDs is an app token's channel allowlist; nil means the
	// principal has none and only membership decides.
	ChannelIDs []string
	// CommandsOnly limits an app viewer to the command.invoked events of its
	// own commands: an app token without the events:read scope receives no
	// channel events at all.
	CommandsOnly bool
	// NoContent withholds the events that carry message bodies (message.created,
	// message.updated, pin.added, alert.updated): an app token needs messages:read
	// as well as events:read to see them.
	NoContent bool
	// TokenID and AppID identify the app token behind a bearer connection
	// (empty for cookie sessions). A session.revoked event naming the token,
	// or the app, closes the connection.
	TokenID string
	AppID   string
	// TokenExpiresAt is the token's expiry in unix ms; 0 means it never
	// expires. The hub closes the connection at that instant.
	TokenExpiresAt int64
}

func (v Viewer) guest() bool { return v.Role == protocol.RoleGuest }

func (v Viewer) allows(channelID string) bool {
	return v.ChannelIDs == nil || slices.Contains(v.ChannelIDs, channelID)
}

// Membership answers the channel questions visibility asks. The hub keeps a
// long-lived cached implementation; a replay builds a fresh one so it sees
// the current state of the database.
//
// Invariant the cache relies on: every write to channels or memberships must
// be committed together with an event of type channel.created,
// channel.member_joined, channel.member_left or membership.updated (whichever
// describes it). The hub invalidates the affected entries when such an event
// passes through it, so a write without one leaves the cache stale.
type Membership interface {
	// ChannelKind returns the channel's kind, or "" when it is unknown.
	ChannelKind(channelID string) string
	// IsMember reports whether userID currently belongs to channelID.
	IsMember(channelID, userID string) bool
	// SharesChannel reports whether a and b are members of a common channel.
	SharesChannel(a, b string) bool
}

// Visible reports whether the viewer may receive e. It is the one rule set
// shared by live delivery and replay (plan pipeline step 2):
//
//   - user-scoped events (read.updated, membership.updated, session.revoked,
//     ephemeral.message) go to the user in e.UserID only; command.invoked
//     goes to the owning app's account, also named by e.UserID;
//   - settings.updated goes to everyone;
//   - user.updated and presence.changed go to every non-guest, to the user
//     themself and to guests sharing a channel with that user;
//   - typing goes to the channel's members except the typist;
//   - channel lifecycle (created, updated, archived, unarchived) goes to
//     the members, and for public channels to every non-guest as well;
//   - channel.member_left also reaches the departing user;
//   - every other channel event goes to the channel's current members.
//
// An app viewer with a channel allowlist never sees events of channels
// outside it; one marked CommandsOnly sees nothing but its own
// command.invoked events. Unknown event types are visible to no one.
func Visible(v Viewer, e Event, m Membership) bool {
	if v.UserID == "" || !Known(e.Type) {
		return false
	}
	if v.CommandsOnly {
		return e.Type == TypeCommandInvoked && e.UserID != "" && e.UserID == v.UserID
	}
	if v.NoContent && carriesContent(e.Type) {
		return false
	}
	if e.ChannelID != "" && !v.allows(e.ChannelID) {
		return false
	}
	switch e.Type {
	case TypeReadUpdated, TypeMembershipUpdated, TypeSessionRevoked, TypeEphemeralMessage, TypeCommandInvoked:
		return e.UserID != "" && e.UserID == v.UserID
	case TypeSettingsUpdated:
		return true
	case TypeUserUpdated, TypePresenceChanged:
		return canSeeUser(v, subjectOf(e), m)
	case TypeTyping:
		return e.ChannelID != "" && e.UserID != v.UserID && m.IsMember(e.ChannelID, v.UserID)
	case TypeChannelCreated, TypeChannelUpdated, TypeChannelArchived, TypeChannelUnarchived:
		if e.ChannelID == "" {
			return false
		}
		if m.IsMember(e.ChannelID, v.UserID) {
			return true
		}
		return !v.guest() && m.ChannelKind(e.ChannelID) == protocol.ChannelPublic
	case TypeChannelMemberLeft:
		if e.ChannelID == "" {
			return false
		}
		return m.IsMember(e.ChannelID, v.UserID) || payloadString(e.Payload, "user_id") == v.UserID
	default:
		return e.ChannelID != "" && m.IsMember(e.ChannelID, v.UserID)
	}
}

// carriesContent lists the event types whose payload holds message content.
func carriesContent(typ string) bool {
	switch typ {
	case TypeMessageCreated, TypeMessageUpdated, TypePinAdded, TypeAlertUpdated:
		return true
	}
	return false
}

// canSeeUser: non-guests see everyone; guests see themselves and the users
// they share a channel with.
func canSeeUser(v Viewer, userID string, m Membership) bool {
	if userID == "" {
		return false
	}
	if userID == v.UserID || !v.guest() {
		return true
	}
	return m.SharesChannel(v.UserID, userID)
}

// subjectOf names the user a user.updated or presence.changed event is
// about: the event's user column, else the payload's id or user_id.
func subjectOf(e Event) string {
	if e.UserID != "" {
		return e.UserID
	}
	if id := payloadString(e.Payload, "id"); id != "" {
		return id
	}
	return payloadString(e.Payload, "user_id")
}

// payloadString returns the top-level string field key of a JSON object
// payload, or "".
func payloadString(payload json.RawMessage, key string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(payload, &m) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(m[key], &s) != nil {
		return ""
	}
	return s
}

// channelInfo is the cached state of one channel.
type channelInfo struct {
	kind    string
	members map[string]bool
}

// Memory bounds of the membership cache. Past them the least recently used
// entry is evicted and reloaded on its next use.
const (
	maxCachedChannels = 10000
	maxCachedUsers    = 10000
)

// MembershipCache is a Membership loaded lazily from the read pool and
// memoized in two LRU maps capped at maxCachedChannels and maxCachedUsers. The
// hub owns one for its lifetime and invalidates entries as membership events
// pass through it; a replay uses a fresh one. Channels that do not exist are
// never memoized, so random ids cannot grow the cache. Load errors fail closed
// (not a member) and are not cached. Safe for concurrent use.
type MembershipCache struct {
	ctx    context.Context
	db     *sql.DB
	logger *slog.Logger

	mu       sync.Mutex
	channels *lru[*channelInfo]
	users    *lru[map[string]bool] // user -> channel ids
}

// NewMembershipCache returns an empty cache over the read pool db. ctx
// bounds every load; logger may be nil.
func NewMembershipCache(ctx context.Context, db *sql.DB, logger *slog.Logger) *MembershipCache {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &MembershipCache{
		ctx: ctx, db: db, logger: logger,
		channels: newLRU[*channelInfo](maxCachedChannels),
		users:    newLRU[map[string]bool](maxCachedUsers),
	}
}

// ChannelKind implements Membership.
func (c *MembershipCache) ChannelKind(channelID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch := c.channel(channelID); ch != nil {
		return ch.kind
	}
	return ""
}

// IsMember implements Membership.
func (c *MembershipCache) IsMember(channelID, userID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.channel(channelID)
	return ch != nil && ch.members[userID]
}

// SharesChannel implements Membership.
func (c *MembershipCache) SharesChannel(a, b string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	ca, cb := c.userChannels(a), c.userChannels(b)
	if len(cb) < len(ca) {
		ca, cb = cb, ca
	}
	for id := range ca {
		if cb[id] {
			return true
		}
	}
	return false
}

// Members returns the current member ids of channelID.
func (c *MembershipCache) Members(channelID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.channel(channelID)
	if ch == nil {
		return nil
	}
	out := make([]string, 0, len(ch.members))
	for id := range ch.members {
		out = append(out, id)
	}
	return out
}

// InvalidateChannel drops the cached state of channelID.
func (c *MembershipCache) InvalidateChannel(channelID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.channels.remove(channelID)
}

// InvalidateUser drops the cached channel list of userID.
func (c *MembershipCache) InvalidateUser(userID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.users.remove(userID)
}

// InvalidateAll empties the cache. The reset lifecycle calls it after the
// content is wiped, so no membership survives the reset.
func (c *MembershipCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.channels.reset()
	c.users.reset()
}

// size returns the number of memoized channels and users.
func (c *MembershipCache) size() (channels, users int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.channels.len(), c.users.len()
}

// channel returns the cached channel, loading it on a miss; nil when the
// channel does not exist or could not be loaded. A nonexistent channel is
// reported but not stored, so the next lookup asks the database again. Callers
// hold c.mu.
func (c *MembershipCache) channel(id string) *channelInfo {
	if id == "" {
		return nil
	}
	if ch, ok := c.channels.get(id); ok {
		return ch
	}
	ch, err := c.loadChannel(id)
	if err != nil {
		c.logger.Warn("realtime: load channel membership", "err", err)
		return nil
	}
	if ch == nil {
		return nil
	}
	c.channels.put(id, ch)
	return ch
}

// loadChannel reads a channel and its members; it returns (nil, nil) when the
// channel does not exist.
func (c *MembershipCache) loadChannel(id string) (*channelInfo, error) {
	ch := &channelInfo{members: map[string]bool{}}
	err := c.db.QueryRowContext(c.ctx, `SELECT kind FROM channels WHERE id = ?`, id).Scan(&ch.kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := c.db.QueryContext(c.ctx, `SELECT user_id FROM memberships WHERE channel_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		ch.members[u] = true
	}
	return ch, rows.Err()
}

// userChannels returns the cached channel set of userID, loading it on a
// miss. Callers hold c.mu.
func (c *MembershipCache) userChannels(userID string) map[string]bool {
	if userID == "" {
		return nil
	}
	if set, ok := c.users.get(userID); ok {
		return set
	}
	set, err := c.loadUser(userID)
	if err != nil {
		c.logger.Warn("realtime: load user memberships", "err", err)
		return nil
	}
	c.users.put(userID, set)
	return set
}

func (c *MembershipCache) loadUser(userID string) (map[string]bool, error) {
	rows, err := c.db.QueryContext(c.ctx, `SELECT channel_id FROM memberships WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		set[id] = true
	}
	return set, rows.Err()
}
