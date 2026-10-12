package onboarding

import "strings"

// OS is a device platform the onboarding page has trust steps for.
type OS string

// Platforms with their own trust steps. OSUnknown opens no section.
const (
	OSUnknown OS = ""
	OSiOS     OS = "ios"
	OSAndroid OS = "android"
	OSWindows OS = "windows"
	OSMac     OS = "macos"
	OSLinux   OS = "linux"
)

// DetectOS guesses the platform from a User-Agent header. It only chooses
// which section opens first; every section stays available because the
// guess can be wrong (iPadOS Safari, for one, reports itself as a Mac).
func DetectOS(ua string) OS {
	switch {
	case containsAny(ua, "iPhone", "iPad", "iPod"):
		return OSiOS
	case strings.Contains(ua, "Android"):
		return OSAndroid
	case strings.Contains(ua, "Windows"):
		return OSWindows
	case containsAny(ua, "Macintosh", "Mac OS X"):
		return OSMac
	case strings.Contains(ua, "CrOS"):
		return OSUnknown
	case containsAny(ua, "Linux", "X11"):
		return OSLinux
	}
	return OSUnknown
}

// IsFirefox reports whether ua is Firefox, which on Linux keeps its own
// certificate store.
func IsFirefox(ua string) bool {
	return strings.Contains(ua, "Firefox/") && !strings.Contains(ua, "Seamonkey/")
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
