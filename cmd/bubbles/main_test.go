package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestDispatchDefaultsToServe(t *testing.T) {
	var got []string
	called := false
	orig := commands["serve"]
	commands["serve"] = func(args []string) int {
		called = true
		got = args
		return 7
	}
	defer func() { commands["serve"] = orig }()

	tests := []struct {
		args []string
		want []string
	}{
		{nil, nil},
		{[]string{}, []string{}},
		{[]string{"--data-dir", "x"}, []string{"--data-dir", "x"}},
		{[]string{"-allow-public"}, []string{"-allow-public"}},
		{[]string{"serve", "--no-mdns"}, []string{"--no-mdns"}},
	}
	for _, tt := range tests {
		called, got = false, nil
		var stderr bytes.Buffer
		if code := dispatch(tt.args, &stderr); code != 7 || !called {
			t.Errorf("dispatch(%q) = %d, serve called = %v", tt.args, code, called)
		}
		if len(got) != len(tt.want) || (len(got) > 0 && !reflect.DeepEqual(got, tt.want)) {
			t.Errorf("dispatch(%q) passed %q to serve, want %q", tt.args, got, tt.want)
		}
	}
}

func TestDispatchUnknownCommand(t *testing.T) {
	var stderr bytes.Buffer
	if code := dispatch([]string{"nope"}, &stderr); code != exitUsage {
		t.Errorf("exit = %d", code)
	}
	for _, want := range []string{`unknown command "nope"`, "cert", "serve", "version"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("usage lacks %q: %q", want, stderr.String())
		}
	}
}
