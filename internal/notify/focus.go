package notify

import "time"

// FocusWindow is how recently a user must have had a visible, focused window
// for the server to hold back Web Push (plan pipeline step 7): the browser
// already shows the message, and a push on top of it would be noise. It covers
// the gap while a window is hidden, a device sleeps or a socket reconnects.
const FocusWindow = 60 * time.Second

// Presence is what the dispatcher needs to know about live connections. The
// realtime hub implements it: connections report window visibility with
// `focus` frames, and presence follows connection activity.
type Presence interface {
	// Online reports whether the user is online (active) now; it decides
	// whether @here reaches them.
	Online(userID string) bool
	// UserFocusedRecently reports whether the user has a connection whose
	// window is visible now, or had one within the given duration.
	UserFocusedRecently(userID string, within time.Duration) bool
}
