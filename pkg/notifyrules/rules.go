// Package notifyrules decides whether one message or alert event should
// notify one recipient (plan pipeline step 6). It is a pure function over
// plain data: the server (Web Push), the tray app and the web app (a mirror
// in web/src/lib/notify/rules.ts) all evaluate it on the same vectors in
// testdata/notify-rules.json. The package uses the standard library only.
package notifyrules

// Message kinds (protocol.Message.Kind).
const (
	KindText   = "text"
	KindAlert  = "alert"
	KindSystem = "system"
)

// Channel kinds.
const (
	ChannelPublic  = "public"
	ChannelPrivate = "private"
	ChannelDM      = "dm"
	ChannelGroupDM = "group_dm"
)

// Notification levels of a membership.
const (
	LevelAll      = "all"
	LevelMentions = "mentions"
	LevelNone     = "none"
)

// Alert severities.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Alert transitions: what an alert event did to its alert. A new alert
// message (message.created, kind alert) is "created"; alert.updated events
// carry one of the others.
const (
	// TransitionCreated: the alert was opened.
	TransitionCreated = "created"
	// TransitionEscalated: a duplicate raised the alert's severity.
	TransitionEscalated = "escalated"
	// TransitionRenotifyDue: the renotify interval elapsed while the alert is
	// open and un-acked.
	TransitionRenotifyDue = "renotify_due"
	// TransitionUpdatedOther: a duplicate that changed nothing worth a
	// notification (occurrence count, last seen, re-rendered body).
	TransitionUpdatedOther = "updated_other"
	// TransitionAcked: a member acknowledged the alert.
	TransitionAcked = "acked"
	// TransitionResolved: the alert was resolved.
	TransitionResolved = "resolved"
)

// Reasons of a Decision. They are for logs and test failures, not for users.
const (
	ReasonSystemMessage      = "system_message"
	ReasonUnknownKind        = "unknown_message_kind"
	ReasonOwnMessage         = "own_message"
	ReasonMuted              = "muted"
	ReasonAlertTransition    = "alert_transition"
	ReasonAlertSeverity      = "alert_severity_invalid"
	ReasonBelowMinSeverity   = "below_alert_min_severity"
	ReasonDirect             = "direct_message"
	ReasonLevelAll           = "level_all"
	ReasonMentioned          = "mentioned"
	ReasonChannelMention     = "channel_mention"
	ReasonHereMention        = "here_mention"
	ReasonCriticalAlert      = "critical_alert"
	ReasonNotMentioned       = "not_mentioned"
	ReasonLevelNone          = "level_none"
	ReasonUnknownLevel       = "unknown_notify_level"
	ReasonUnknownMinSeverity = "alert_min_severity_invalid"
)

// Mentions mirrors protocol.Mentions: who a message addresses.
type Mentions struct {
	UserIDs []string `json:"user_ids"`
	Channel bool     `json:"channel"`
	Here    bool     `json:"here"`
}

// Input is everything Decide looks at. Times are unix milliseconds.
type Input struct {
	// RecipientID and AuthorID are the user ids of the person who would be
	// notified and of the message's author. A recipient is never notified
	// about their own message.
	RecipientID string `json:"recipient_id"`
	AuthorID    string `json:"author_id"`
	// ChannelKind is public, private, dm or group_dm.
	ChannelKind string `json:"channel_kind"`
	// NotifyLevel (all, mentions, none), AlertMinSeverity (info, warning,
	// critical) and MutedUntil (nil: not muted) are the recipient's
	// membership settings for the channel.
	NotifyLevel      string `json:"notify_level"`
	AlertMinSeverity string `json:"alert_min_severity"`
	MutedUntil       *int64 `json:"muted_until"`
	// Now is the evaluation time. A mute lasts while Now < MutedUntil.
	Now int64 `json:"now"`
	// Mentions are the message's mentions.
	Mentions Mentions `json:"mentions"`
	// RecipientOnline is whether the recipient is online (active), which
	// decides whether @here reaches them.
	RecipientOnline bool `json:"recipient_online"`
	// MessageKind is text, alert or system.
	MessageKind string `json:"message_kind"`
	// AlertTransition and AlertSeverity describe an alert message
	// (MessageKind alert); they are ignored for other kinds.
	AlertTransition string `json:"alert_transition"`
	AlertSeverity   string `json:"alert_severity"`
}

// Decision is the verdict of Decide. Reason names the rule that decided.
type Decision struct {
	Notify bool   `json:"notify"`
	Reason string `json:"reason"`
}

// Decide applies the notification rules in this order, the first to match
// decides:
//
//  1. system messages never notify;
//  2. never notify the author;
//  3. a muted membership (Now < MutedUntil) gets nothing, not even critical
//     alerts;
//  4. an alert message notifies only on creation, escalation or renotify due
//     (acked, resolved and other updates do not) and only if its severity is
//     at least the member's alert_min_severity;
//  5. direct messages and group DMs notify;
//  6. notify_level all notifies;
//  7. notify_level mentions notifies when the recipient is mentioned by id,
//     @channel is used, @here is used and the recipient is online, or the
//     message is a critical alert (which passes the mentions level but never
//     a mute);
//  8. notify_level none, and anything unrecognised, does not notify.
func Decide(in Input) Decision {
	switch in.MessageKind {
	case KindSystem:
		return no(ReasonSystemMessage)
	case KindText, KindAlert:
	default:
		return no(ReasonUnknownKind)
	}
	if in.RecipientID != "" && in.RecipientID == in.AuthorID {
		return no(ReasonOwnMessage)
	}
	if in.MutedUntil != nil && in.Now < *in.MutedUntil {
		return no(ReasonMuted)
	}
	critical := false
	if in.MessageKind == KindAlert {
		switch in.AlertTransition {
		case TransitionCreated, TransitionEscalated, TransitionRenotifyDue:
		default:
			return no(ReasonAlertTransition)
		}
		sev, ok := severityRank(in.AlertSeverity)
		if !ok {
			return no(ReasonAlertSeverity)
		}
		floor, ok := severityRank(in.AlertMinSeverity)
		if !ok {
			return no(ReasonUnknownMinSeverity)
		}
		if sev < floor {
			return no(ReasonBelowMinSeverity)
		}
		critical = in.AlertSeverity == SeverityCritical
	}
	if in.ChannelKind == ChannelDM || in.ChannelKind == ChannelGroupDM {
		return yes(ReasonDirect)
	}
	switch in.NotifyLevel {
	case LevelAll:
		return yes(ReasonLevelAll)
	case LevelMentions:
		switch {
		case mentions(in.Mentions.UserIDs, in.RecipientID):
			return yes(ReasonMentioned)
		case in.Mentions.Channel:
			return yes(ReasonChannelMention)
		case in.Mentions.Here && in.RecipientOnline:
			return yes(ReasonHereMention)
		case critical:
			return yes(ReasonCriticalAlert)
		}
		return no(ReasonNotMentioned)
	case LevelNone:
		return no(ReasonLevelNone)
	}
	return no(ReasonUnknownLevel)
}

func yes(reason string) Decision { return Decision{Notify: true, Reason: reason} }
func no(reason string) Decision  { return Decision{Reason: reason} }

func mentions(ids []string, id string) bool {
	if id == "" {
		return false
	}
	for _, m := range ids {
		if m == id {
			return true
		}
	}
	return false
}

func severityRank(s string) (int, bool) {
	switch s {
	case SeverityInfo:
		return 0, true
	case SeverityWarning:
		return 1, true
	case SeverityCritical:
		return 2, true
	}
	return 0, false
}
