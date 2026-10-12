package main

import (
	"log/slog"
	"strconv"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// notifier shows native notifications through the Wails v3 notifications
// service (toasts on Windows, Notification Center on macOS, the freedesktop
// daemon on Linux). The service exists on all three platforms in the pinned
// version, so no fallback library is needed.
type notifier struct {
	svc    *notifications.NotificationService
	logger *slog.Logger
	seq    atomic.Uint64
}

func newNotifier(logger *slog.Logger) *notifier {
	return &notifier{svc: notifications.New(), logger: logger}
}

// authorize asks the OS for permission where it is required (macOS).
func (n *notifier) authorize() {
	if ok, err := n.svc.CheckNotificationAuthorization(); err == nil && ok {
		return
	}
	if _, err := n.svc.RequestNotificationAuthorization(); err != nil {
		n.logger.Warn("notification authorization failed", "err", err)
	}
}

// show raises one notification. Only the channel id travels in the data so a
// click can open the right conversation; message text is never logged.
func (n *notifier) show(id, title, body, channelID string, critical bool) {
	if id == "" {
		id = "bubbles-" + strconv.FormatUint(n.seq.Add(1), 10)
	}
	opts := notifications.NotificationOptions{ID: id, Title: title, Body: body}
	if channelID != "" {
		opts.Data = map[string]interface{}{"channel_id": channelID}
	}
	if critical {
		opts.InterruptionLevel = notifications.InterruptionLevelTimeSensitive
	}
	if err := n.svc.SendNotification(opts); err != nil {
		n.logger.Warn("show notification failed", "err", err)
	}
}

// onClick registers the handler for a clicked notification.
func (n *notifier) onClick(fn func(channelID string)) {
	n.svc.OnNotificationResponse(func(r notifications.NotificationResult) {
		if r.Error != nil {
			return
		}
		id, _ := r.Response.UserInfo["channel_id"].(string)
		fn(id)
	})
}
