//go:build !race

package reset

import "time"

// resetSpeedLimit is the budget of TestResetWith2000FilesIsFast.
const resetSpeedLimit = 5 * time.Second
