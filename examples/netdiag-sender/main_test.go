package main

import (
	"testing"
)

func TestMapNotice(t *testing.T) {
	tests := []struct {
		name    string
		in      Notice
		sev     string
		key     string
		resolve bool
		fields  int
	}{
		{"warn", Notice{Trigger: "printerOffline", Severity: "warn", IP: "1.2.3.4", Title: "T", Message: "m", At: "2026-01-01T00:00:00Z"}, "warning", "printerOffline:1.2.3.4", false, 2},
		{"critical with key", Notice{Trigger: "ipConflict", Severity: "critical", IP: "1.2.3.4", Key: "k1"}, "critical", "k1", false, 2},
		{"info", Notice{Trigger: "newDevice", Severity: "info"}, "info", "newDevice:", false, 1},
		{"unknown severity", Notice{Trigger: "x", Severity: "weird", IP: "1.1.1.1"}, "info", "x:1.1.1.1", false, 2},
		{"ok severity", Notice{Trigger: "printerOffline", Severity: "ok", IP: "1.2.3.4"}, "", "printerOffline:1.2.3.4", true, 0},
		{"ok flag", Notice{Trigger: "t", Severity: "warn", IP: "9.9.9.9", Key: "kk", OK: true}, "", "kk", true, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, resolve := mapNotice(tc.in, "ch")
			if resolve != tc.resolve || a.DedupeKey != tc.key || a.Severity != tc.sev || len(a.Fields) != tc.fields || a.Channel != "ch" {
				t.Fatalf("got %+v resolve=%v", a, resolve)
			}
		})
	}
	a, _ := mapNotice(Notice{Trigger: "t", Title: "", At: "2026-01-01T00:00:00Z"}, "c")
	if a.Title != "t" || a.OccurredAt == nil || *a.OccurredAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("title/occurred_at: %+v", a)
	}
}
