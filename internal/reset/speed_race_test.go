//go:build race

package reset

import "time"

// resetSpeedLimit is the budget of TestResetWith2000FilesIsFast; the race
// detector slows file operations several times over.
const resetSpeedLimit = 20 * time.Second
