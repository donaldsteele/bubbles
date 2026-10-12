//go:build windows

package service

import (
	"errors"
	"testing"
)

func TestRollbackCreatedDeletesAndReturnsCause(t *testing.T) {
	cause := errors.New("firewall failed")
	tests := []struct {
		name      string
		delErr    error
		wantCause bool
		wantJoin  bool
	}{
		{name: "delete ok returns cause", delErr: nil, wantCause: true},
		{name: "delete failure joins both", delErr: errors.New("marked for deletion"), wantCause: true, wantJoin: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deleted := 0
			got := rollbackCreated(func() error {
				deleted++
				return tc.delErr
			}, cause)
			if deleted != 1 {
				t.Fatalf("delete called %d times, want 1", deleted)
			}
			if !errors.Is(got, cause) {
				t.Fatalf("returned error %v does not wrap cause", got)
			}
			if tc.wantJoin && !errors.Is(got, tc.delErr) {
				t.Fatalf("returned error %v does not wrap delete failure", got)
			}
			if !tc.wantJoin && got != cause {
				t.Fatalf("returned %v, want the cause unchanged", got)
			}
		})
	}
}
