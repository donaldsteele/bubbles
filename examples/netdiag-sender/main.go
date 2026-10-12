// Command netdiag-sender is the reference mapping from netdiag-style Notice
// JSON lines to bubbles alerts. It reads one Notice per line from stdin (or
// emits a demo sequence with --demo) and sends them through an AlertQueue, so
// alerts survive brief server outages. An "OK again" notice resolves the
// alert with the same dedupe key.
//
//	netdiag-sender --conn 'bubbles://192.168.1.20:28225/?token=bub_...&fp=sha256:...' < notices.jsonl
//	netdiag-sender --pair https://192.168.1.20:28225 --onboard http://192.168.1.20:28226 --demo
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/donaldsteele/bubbles/pkg/bubblesclient"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Notice is the netdiag notification record.
type Notice struct {
	Trigger  string `json:"trigger"`
	Severity string `json:"severity"` // info | warn | critical | ok
	IP       string `json:"ip"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	At       string `json:"at"`  // RFC 3339
	Key      string `json:"key"` // dedupe key
	// OK marks an "OK again" transition (also: severity "ok").
	OK bool `json:"ok"`
}

// mapNotice converts a Notice to an alert. resolve reports an "OK again"
// notice, which resolves the alert with the returned dedupe key.
func mapNotice(n Notice, channel string) (alert protocol.AlertInput, resolve bool) {
	key := n.Key
	if key == "" {
		key = n.Trigger + ":" + n.IP
	}
	if n.OK || strings.EqualFold(n.Severity, "ok") {
		return protocol.AlertInput{Channel: channel, DedupeKey: key, Text: n.Message}, true
	}
	title := n.Title
	if title == "" {
		title = n.Trigger
	}
	a := protocol.AlertInput{
		Channel:   channel,
		Severity:  mapSeverity(n.Severity),
		Title:     title,
		Text:      n.Message,
		DedupeKey: key,
	}
	if n.IP != "" {
		a.Fields = append(a.Fields, protocol.AlertField{Name: "IP", Value: n.IP, Short: true})
	}
	if n.Trigger != "" {
		a.Fields = append(a.Fields, protocol.AlertField{Name: "Trigger", Value: n.Trigger, Short: true})
	}
	if n.At != "" {
		at := n.At
		a.OccurredAt = &at
	}
	return a, false
}

func mapSeverity(s string) string {
	switch strings.ToLower(s) {
	case "critical":
		return "critical"
	case "warn", "warning":
		return "warning"
	default:
		return "info"
	}
}

// demoNotices is the --demo sequence.
func demoNotices(now time.Time) []Notice {
	at := func(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339) }
	return []Notice{
		{Trigger: "printerOffline", Severity: "warn", IP: "192.168.1.50", Title: "Printer offline", Message: "No SNMP reply for 3 polls", At: at(-5 * time.Minute)},
		{Trigger: "printerOffline", Severity: "warn", IP: "192.168.1.50", Title: "Printer offline", Message: "Still no reply (10 polls)", At: at(-2 * time.Minute)},
		{Trigger: "supplyLow", Severity: "warn", IP: "192.168.1.50", Title: "Toner low", Message: "Black toner at 8%", At: at(-time.Minute), Key: "supplyLow:192.168.1.50:black"},
		{Trigger: "newDevice", Severity: "info", IP: "192.168.1.77", Title: "New device", Message: "Unknown MAC joined the network", At: at(-30 * time.Second)},
		{Trigger: "ipConflict", Severity: "critical", IP: "192.168.1.20", Title: "IP address conflict", Message: "Two devices claim 192.168.1.20", At: at(-10 * time.Second)},
		{Trigger: "printerOffline", Severity: "ok", IP: "192.168.1.50", Title: "Printer online", Message: "Printer is reachable again", At: at(0)},
	}
}

type options struct {
	conn, pair, onboard, fp, save, channel string
	demo                                   bool
}

func main() {
	var o options
	flag.StringVar(&o.conn, "conn", os.Getenv("BUBBLES_CONN"), "connection string bubbles://host:port/?token=...&fp=sha256:... (or env BUBBLES_CONN)")
	flag.StringVar(&o.pair, "pair", "", "HTTPS server URL to pair with, for example https://192.168.1.20:28225")
	flag.StringVar(&o.onboard, "onboard", "", "onboarding HTTP URL used to fetch the CA, for example http://192.168.1.20:28226")
	flag.StringVar(&o.fp, "fp", "", "expected CA fingerprint; skips the interactive confirmation when pairing")
	flag.StringVar(&o.save, "save", "", "after pairing, write the connection string to this file (mode 0600)")
	flag.StringVar(&o.channel, "channel", "netdiag-alerts", "channel name or id for alerts")
	flag.BoolVar(&o.demo, "demo", false, "emit a demo sequence instead of reading stdin")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, o, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "netdiag-sender:", err)
		if errors.Is(err, bubblesclient.ErrUnauthorized) || errors.Is(err, bubblesclient.ErrServerIdentityChanged) {
			os.Exit(3) // the app must re-pair
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, o options, stdin io.Reader, stdout, stderr io.Writer) error {
	cfg, err := connect(ctx, o, stdin, stdout, stderr)
	if err != nil {
		return err
	}
	c, err := bubblesclient.New(cfg)
	if err != nil {
		return err
	}
	q := bubblesclient.NewAlertQueue(c, 0)
	defer q.Close()
	q.SetOnStop(func(err error) { fmt.Fprintln(stderr, "alert queue stopped, re-pair required:", err) })

	enqueue := func(n Notice) error {
		a, resolve := mapNotice(n, o.channel)
		if resolve {
			return q.EnqueueResolve(a.DedupeKey, a.Text)
		}
		return q.Enqueue(a)
	}
	if o.demo {
		for _, n := range demoNotices(time.Now()) {
			if err := enqueue(n); err != nil {
				return err
			}
		}
	} else {
		sc := bufio.NewScanner(stdin)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var n Notice
			if err := json.Unmarshal([]byte(line), &n); err != nil {
				fmt.Fprintln(stderr, "skipping malformed notice line:", err)
				continue
			}
			if err := enqueue(n); err != nil {
				return err
			}
			if ctx.Err() != nil {
				break
			}
		}
		if err := sc.Err(); err != nil {
			return err
		}
	}
	fctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := q.Flush(fctx); err != nil {
		return fmt.Errorf("deliver alerts: %w", err)
	}
	return nil
}

// connect resolves the connection from --conn or by pairing.
func connect(ctx context.Context, o options, stdin io.Reader, stdout, stderr io.Writer) (bubblesclient.Config, error) {
	if o.conn != "" {
		return bubblesclient.ParseConnectionString(o.conn)
	}
	if o.pair == "" || o.onboard == "" {
		return bubblesclient.Config{}, errors.New("pass --conn, or --pair and --onboard to pair with a server")
	}
	_, fp, err := bubblesclient.FetchCA(ctx, o.onboard)
	if err != nil {
		return bubblesclient.Config{}, err
	}
	fmt.Fprintf(stderr, "Server CA fingerprint (compare with the admin UI):\n  %s\n", bubblesclient.FormatFingerprint(fp))
	if o.fp != "" {
		want, err := bubblesclient.NormalizeFingerprint(o.fp)
		if err != nil || want != fp {
			return bubblesclient.Config{}, bubblesclient.ErrServerIdentityChanged
		}
	} else if !confirm(stdin, stderr) {
		return bubblesclient.Config{}, errors.New("fingerprint not confirmed; pass --fp to confirm non-interactively")
	}
	p, err := bubblesclient.RequestPairing(ctx, o.pair, fp, protocol.PairingRequest{
		AppName: "netdiag", Description: "netdiag alert sender", RequestedScopes: []string{protocol.ScopeAlertsWrite},
	})
	if err != nil {
		return bubblesclient.Config{}, err
	}
	fmt.Fprintf(stderr, "Pairing code: %s  (approve it in the bubbles admin UI)\n", p.Code())
	cfg, err := p.Wait(ctx)
	if err != nil {
		return bubblesclient.Config{}, err
	}
	if o.save != "" {
		s, err := cfg.ConnectionString()
		if err != nil {
			return bubblesclient.Config{}, err
		}
		if err := os.WriteFile(o.save, []byte(s+"\n"), 0o600); err != nil {
			return bubblesclient.Config{}, err
		}
		fmt.Fprintln(stderr, "connection string saved to", o.save)
	}
	return cfg, nil
}

// confirm asks the operator on an interactive terminal.
func confirm(stdin io.Reader, stderr io.Writer) bool {
	f, ok := stdin.(*os.File)
	if !ok {
		return false
	}
	if st, err := f.Stat(); err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	fmt.Fprint(stderr, "Does it match? [y/N] ")
	line, _ := bufio.NewReader(f).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y")
}
