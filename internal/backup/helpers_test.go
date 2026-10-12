package backup

import (
	"encoding/json"
	"runtime"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func runtimeIsWindows() bool { return runtime.GOOS == "windows" }
