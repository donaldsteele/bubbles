package main

import (
	"strings"
	"testing"
	"time"
)

func TestPercentileNearestRank(t *testing.T) {
	ms := func(n ...int) []time.Duration {
		out := make([]time.Duration, len(n))
		for i, v := range n {
			out[i] = time.Duration(v) * time.Millisecond
		}
		return out
	}
	tests := []struct {
		name   string
		sorted []time.Duration
		p      float64
		want   time.Duration
	}{
		{"empty is zero", nil, 0.95, 0},
		{"single sample", ms(7), 0.50, 7 * time.Millisecond},
		{"p50 of four", ms(1, 2, 3, 4), 0.50, 2 * time.Millisecond},
		{"p95 of twenty", ms(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20), 0.95, 19 * time.Millisecond},
		{"p100 is max", ms(1, 2, 3), 1.0, 3 * time.Millisecond},
		{"p0 clamps to first", ms(5, 9), 0, 5 * time.Millisecond},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := percentile(tc.sorted, tc.p); got != tc.want {
				t.Fatalf("percentile(%v) = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}

func TestSummarizeSortsAndReturnsMax(t *testing.T) {
	lat := []time.Duration{3 * time.Millisecond, time.Millisecond, 2 * time.Millisecond}
	p50, _, _, max := summarize(lat)
	if p50 != 2*time.Millisecond || max != 3*time.Millisecond {
		t.Fatalf("p50=%v max=%v, want 2ms and 3ms", p50, max)
	}
	if z50, _, _, zmax := summarize(nil); z50 != 0 || zmax != 0 {
		t.Fatalf("empty summarize = %v/%v, want zeros", z50, zmax)
	}
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"", 0, false},
		{"300MB", 300 << 20, false},
		{"300mb", 300 << 20, false},
		{" 1GB ", 1 << 30, false},
		{"512KB", 512 << 10, false},
		{"1.5MB", 3 << 19, false},
		{"42B", 42, false},
		{"4096", 4096, false},
		{"-1MB", 0, true},
		{"abcMB", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseSize(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseSize(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("parseSize(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestFailuresDetectViolations(t *testing.T) {
	const mb = 1 << 20
	tests := []struct {
		name       string
		r          result
		assertP95  time.Duration
		assertRSS  int64
		wantSubstr []string
	}{
		{
			name:      "clean run passes",
			r:         result{Sent: 10, Received: 10, Expected: 10, P95: 10 * time.Millisecond, PeakRSS: 100 * mb},
			assertP95: 150 * time.Millisecond,
			assertRSS: 300 * mb,
		},
		{
			name:       "loss is reported",
			r:          result{Sent: 10, Lost: 2},
			wantSubstr: []string{"2 messages lost"},
		},
		{
			name:       "duplicates and seq regressions are reported",
			r:          result{Sent: 10, Duplicates: 1, SeqRegressions: 3},
			wantSubstr: []string{"1 duplicate deliveries", "3 seq regressions"},
		},
		{
			name:       "send errors and no traffic are reported",
			r:          result{SendErrors: 4},
			wantSubstr: []string{"4 send errors", "no messages sent"},
		},
		{
			name:       "p95 over the assertion fails",
			r:          result{Sent: 1, P95: 200 * time.Millisecond},
			assertP95:  150 * time.Millisecond,
			wantSubstr: []string{"p95 200ms exceeds 150ms"},
		},
		{
			name:       "rss over the assertion fails",
			r:          result{Sent: 1, PeakRSS: 301 * mb},
			assertRSS:  300 * mb,
			wantSubstr: []string{"exceeds"},
		},
		{
			name:       "unknown rss with an rss assertion fails",
			r:          result{Sent: 1},
			assertRSS:  300 * mb,
			wantSubstr: []string{"RSS unknown"},
		},
		{
			name:      "assertions off ignore latency and rss",
			r:         result{Sent: 1, P95: time.Hour, PeakRSS: 10 * 1024 * mb},
			assertP95: 0,
			assertRSS: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.r.failures(tc.assertP95, tc.assertRSS)
			if len(tc.wantSubstr) == 0 {
				if len(got) != 0 {
					t.Fatalf("failures = %v, want none", got)
				}
				return
			}
			joined := strings.Join(got, "; ")
			for _, want := range tc.wantSubstr {
				if !strings.Contains(joined, want) {
					t.Fatalf("failures = %q, want to contain %q", joined, want)
				}
			}
		})
	}
}
