package protocol_test

import (
	"testing"

	"github.com/donaldsteele/bubbles/pkg/notifyrules"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// pkg/notifyrules is standard-library only, so it repeats the alert
// transition names; they must stay equal to the wire constants.
func TestAlertTransitionsMatchNotifyRules(t *testing.T) {
	for name, pair := range map[string][2]string{
		"created":       {protocol.AlertTransitionCreated, notifyrules.TransitionCreated},
		"escalated":     {protocol.AlertTransitionEscalated, notifyrules.TransitionEscalated},
		"renotify_due":  {protocol.AlertTransitionRenotifyDue, notifyrules.TransitionRenotifyDue},
		"updated_other": {protocol.AlertTransitionUpdatedOther, notifyrules.TransitionUpdatedOther},
		"acked":         {protocol.AlertTransitionAcked, notifyrules.TransitionAcked},
		"resolved":      {protocol.AlertTransitionResolved, notifyrules.TransitionResolved},
	} {
		if pair[0] != name || pair[1] != name {
			t.Errorf("%s: protocol=%q notifyrules=%q", name, pair[0], pair[1])
		}
	}
}
