package autostart

import "os"

func liveEnabled() bool { return os.Getenv("BUBBLES_LIVE_AUTOSTART") == "1" }
