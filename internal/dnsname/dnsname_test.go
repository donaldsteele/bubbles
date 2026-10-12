package dnsname

import "testing"

func TestValid(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"example.lan", true}, {"a", true}, {"", false}, {"10.0.0.1", false},
		{"-a.lan", false}, {"a-.lan", false}, {"a_b.lan", false}, {"a..lan", false},
		{"UPPER.lan", false},
	} {
		if got := Valid(tc.name) == nil; got != tc.ok {
			t.Errorf("Valid(%q) ok=%v, want %v", tc.name, got, tc.ok)
		}
	}
}
