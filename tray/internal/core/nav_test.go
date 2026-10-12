package core

import "testing"

func TestChannelPath(t *testing.T) {
	for _, tc := range []struct{ id, want string }{
		{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "/c/01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		{"", ""},
		{"../x", ""},
		{"a/b", ""},
		{"a?b", ""},
		{"a b", ""},
		{"//evil", ""},
		{"x\n", ""},
		{string(make([]byte, 65)), ""},
	} {
		if got := ChannelPath(tc.id); got != tc.want {
			t.Errorf("ChannelPath(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}
