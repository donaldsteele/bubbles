package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

const (
	testMCPToken = "bub_SECRETSECRETSECRETSECRET"
	testMCPFP    = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testMCPConn  = "bubbles://127.0.0.1:28225/?token=" + testMCPToken + "&fp=" + testMCPFP
)

func runMCPTest(t *testing.T, env map[string]string, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code = mcpCmd(ctx, args, func(k string) string { return env[k] }, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestMCPCommandArguments(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		args []string
		want int
	}{
		{"no connection", nil, nil, 2},
		{"unknown flag", nil, []string{"--nope"}, 2},
		{"stray argument", nil, []string{"--conn", testMCPConn, "x"}, 2},
		{"bad connection string", nil, []string{"--conn", "https://x/?token=" + testMCPToken}, 2},
		{"no fingerprint", nil, []string{"--conn", "bubbles://127.0.0.1:28225/?token=" + testMCPToken}, 2},
		{"flag", nil, []string{"--conn", testMCPConn}, 0},
		{"environment", map[string]string{connEnv: testMCPConn}, nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runMCPTest(t, tc.env, "", tc.args...)
			if code != tc.want {
				t.Fatalf("exit = %d, want %d; stderr %q", code, tc.want, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want only MCP frames (none here)", stdout)
			}
			if strings.Contains(stderr, testMCPToken) {
				t.Errorf("stderr leaks the token: %q", stderr)
			}
		})
	}
}
