package main

import (
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// percentile returns the p-th percentile (0..1) of sorted using the
// nearest-rank method; sorted must be ascending.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

// parseSize parses sizes such as "300MB", "1GB" or "512KB" (powers of 1024).
func parseSize(s string) (int64, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	if t == "" {
		return 0, nil
	}
	mult := int64(1)
	for _, e := range []struct {
		suffix string
		m      int64
	}{{"KB", 1 << 10}, {"MB", 1 << 20}, {"GB", 1 << 30}, {"B", 1}} {
		if strings.HasSuffix(t, e.suffix) {
			mult = e.m
			t = strings.TrimSuffix(t, e.suffix)
			break
		}
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return int64(n * float64(mult)), nil
}

// rssBytes reads the resident set (Windows: working set) of a process.
func rssBytes(pid int) (int64, error) {
	switch runtime.GOOS {
	case "windows":
		out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH").Output()
		if err != nil {
			return 0, err
		}
		fields := strings.Split(strings.TrimSpace(string(out)), "\",\"")
		if len(fields) < 5 {
			return 0, fmt.Errorf("process %d not found", pid)
		}
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, fields[len(fields)-1])
		kb, err := strconv.ParseInt(digits, 10, 64)
		if err != nil {
			return 0, err
		}
		return kb * 1024, nil
	case "linux":
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
		if err != nil {
			return 0, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				f := strings.Fields(line)
				if len(f) >= 2 {
					kb, err := strconv.ParseInt(f[1], 10, 64)
					return kb * 1024, err
				}
			}
		}
		return 0, fmt.Errorf("VmRSS not found")
	default:
		out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return 0, err
		}
		kb, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		return kb * 1024, err
	}
}

// rssSampler records the peak RSS of a process until stop is closed.
type rssSampler struct {
	peak  atomic.Int64
	first atomic.Int64
	done  chan struct{}
}

func startRSSSampler(pid int, every time.Duration, stop <-chan struct{}) *rssSampler {
	s := &rssSampler{done: make(chan struct{})}
	sample := func() {
		if v, err := rssBytes(pid); err == nil {
			s.first.CompareAndSwap(0, v)
			if v > s.peak.Load() {
				s.peak.Store(v)
			}
		}
	}
	go func() {
		defer close(s.done)
		sample()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-stop:
				sample()
				return
			case <-t.C:
				sample()
			}
		}
	}()
	return s
}

// result is the aggregate outcome of one run.
type result struct {
	Clients, Senders, Channels int
	Duration                   time.Duration
	Sent, SendErrors           int64
	Received, Expected         int64
	Lost, Duplicates           int64
	SeqRegressions             int64
	ClientsIncomplete          int
	P50, P95, P99, Max         time.Duration
	PeakRSS, StartRSS          int64
}

func summarize(lat []time.Duration) (p50, p95, p99, max time.Duration) {
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	if len(lat) == 0 {
		return
	}
	return percentile(lat, 0.50), percentile(lat, 0.95), percentile(lat, 0.99), lat[len(lat)-1]
}

func (r result) print(w io.Writer) {
	fmt.Fprintf(w, "clients=%d senders=%d channels=%d duration=%s\n", r.Clients, r.Senders, r.Channels, r.Duration)
	fmt.Fprintf(w, "sent=%d send_errors=%d received=%d expected=%d lost=%d duplicates=%d seq_regressions=%d clients_incomplete=%d\n",
		r.Sent, r.SendErrors, r.Received, r.Expected, r.Lost, r.Duplicates, r.SeqRegressions, r.ClientsIncomplete)
	fmt.Fprintf(w, "latency p50=%s p95=%s p99=%s max=%s\n", r.P50.Round(time.Microsecond), r.P95.Round(time.Microsecond), r.P99.Round(time.Microsecond), r.Max.Round(time.Microsecond))
	if r.PeakRSS > 0 {
		fmt.Fprintf(w, "server_rss_start=%.1f MiB server_rss_peak=%.1f MiB\n", float64(r.StartRSS)/(1<<20), float64(r.PeakRSS)/(1<<20))
	} else {
		fmt.Fprintln(w, "server_rss_peak=unknown")
	}
}

// failures lists every violated assertion; empty means the run passed.
func (r result) failures(assertP95 time.Duration, assertRSS int64) []string {
	var f []string
	if r.Lost > 0 {
		f = append(f, fmt.Sprintf("%d messages lost", r.Lost))
	}
	if r.Duplicates > 0 {
		f = append(f, fmt.Sprintf("%d duplicate deliveries", r.Duplicates))
	}
	if r.SeqRegressions > 0 {
		f = append(f, fmt.Sprintf("%d seq regressions", r.SeqRegressions))
	}
	if r.SendErrors > 0 {
		f = append(f, fmt.Sprintf("%d send errors", r.SendErrors))
	}
	if r.Sent == 0 {
		f = append(f, "no messages sent")
	}
	if assertP95 > 0 && r.P95 > assertP95 {
		f = append(f, fmt.Sprintf("p95 %s exceeds %s", r.P95, assertP95))
	}
	if assertRSS > 0 {
		if r.PeakRSS == 0 {
			f = append(f, "server RSS unknown but --assert-rss given")
		} else if r.PeakRSS > assertRSS {
			f = append(f, fmt.Sprintf("server RSS %d bytes exceeds %d", r.PeakRSS, assertRSS))
		}
	}
	return f
}
