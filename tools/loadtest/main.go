// Command loadtest drives a bubbles server with many realtime clients and
// reports send-to-receive latency, message loss and server memory.
//
//	go run ./tools/loadtest --clients 200 --senders 20 --rate 5 --duration 60s \
//	    --assert-p95 150ms --assert-rss 300MB
//
// Without --url it builds ./cmd/bubbles into a temporary directory, starts it
// with a throwaway data directory on random loopback ports and tears it down
// afterwards. Every message carries its send timestamp in the body; receivers
// compute the latency from that timestamp (Unix nanoseconds, read from the
// same host clock). Latency is therefore only meaningful when the generator
// and the server run on one host; do not compare numbers taken across hosts.
// Loss is decided by comparing message-ID sets: every ID posted to a channel
// must appear in each subscribed client's set.
//
// Seq gaps are not counted as loss. The seq is global and the server filters
// each replay and live event by visibility, so a client sees gaps by design.
// Only a seq that does not increase (a regression) is reported.
package main

import (
	"context"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

type options struct {
	url        string
	caFile     string
	insecure   bool
	adminUser  string
	adminPass  string
	serverPID  int
	clients    int
	senders    int
	channels   int
	rate       float64
	duration   time.Duration
	assertP95  time.Duration
	assertRSS  string
	keepDir    string
	serverArgs string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "loadtest:", err)
		os.Exit(1)
	}
}

func run() error {
	var o options
	flag.StringVar(&o.url, "url", "", "target server https URL; empty starts a private server")
	flag.StringVar(&o.caFile, "ca", "", "CA certificate (PEM) to trust when --url is given")
	flag.BoolVar(&o.insecure, "insecure", false, "skip TLS verification when --url is given")
	flag.StringVar(&o.adminUser, "admin-user", "", "admin username when --url is given (an existing admin)")
	flag.StringVar(&o.adminPass, "admin-password", "", "admin password when --url is given")
	flag.IntVar(&o.serverPID, "server-pid", 0, "PID of the --url server, for RSS sampling")
	flag.IntVar(&o.clients, "clients", 200, "WebSocket clients (one user each)")
	flag.IntVar(&o.senders, "senders", 20, "clients that post messages")
	flag.IntVar(&o.channels, "channels", 20, "channels the clients are spread over")
	flag.Float64Var(&o.rate, "rate", 5, "messages per second per sender")
	flag.DurationVar(&o.duration, "duration", 60*time.Second, "how long senders post")
	flag.DurationVar(&o.assertP95, "assert-p95", 0, "fail when p95 latency exceeds this (0 = off)")
	flag.StringVar(&o.assertRSS, "assert-rss", "", "fail when peak server RSS exceeds this, e.g. 300MB (powers of 1024)")
	flag.StringVar(&o.keepDir, "keep-dir", "", "keep the private server's data and log under this directory")
	flag.StringVar(&o.serverArgs, "server-args", "", "extra space-separated flags for the private server")
	flag.Parse()

	if o.clients < 1 || o.channels < 1 || o.senders < 0 || o.senders > o.clients || o.rate <= 0 {
		return errors.New("invalid --clients/--senders/--channels/--rate")
	}
	if o.channels > o.clients {
		o.channels = o.clients
	}
	assertRSS, err := parseSize(o.assertRSS)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var (
		base  string
		pool  *x509.CertPool
		pid   = o.serverPID
		admin = &user{name: o.adminUser, password: o.adminPass}
	)
	if o.url == "" {
		srv, err := startServer(ctx, &o)
		if err != nil {
			return err
		}
		defer srv.stop()
		base, pool, pid = srv.base, srv.pool, srv.pid
		if err := srv.setup(ctx, admin); err != nil {
			return fmt.Errorf("setup: %w", err)
		}
		fmt.Printf("server: %s (pid %d)\n", base, pid)
		if srv.keep != "" {
			fmt.Printf("server data and log kept in %s\n", srv.keep)
		}
	} else {
		base = o.url
		if o.caFile != "" {
			pem, err := os.ReadFile(o.caFile)
			if err != nil {
				return err
			}
			pool = x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return errors.New("--ca: no certificate found")
			}
		}
	}
	a := newAPI(base, pool, o.insecure)
	if o.url != "" {
		set, err := a.call(ctx, "POST", "/api/v1/auth/login", "", protocol.LoginRequest{Username: admin.name, Password: admin.password}, nil, 200)
		if err != nil {
			return fmt.Errorf("admin login: %w", err)
		}
		admin.cookie = set
	}

	users, chanIDs, err := populate(ctx, a, admin, &o)
	if err != nil {
		return fmt.Errorf("populate: %w", err)
	}
	fmt.Printf("created %d users in %d channels\n", len(users), len(chanIDs))

	var sampler *rssSampler
	stopRSS := make(chan struct{})
	if pid > 0 {
		sampler = startRSSSampler(pid, 500*time.Millisecond, stopRSS)
	}

	res, err := drive(ctx, a, users, chanIDs, &o)
	close(stopRSS)
	if sampler != nil {
		<-sampler.done
		res.PeakRSS, res.StartRSS = sampler.peak.Load(), sampler.first.Load()
	}
	if err != nil {
		return err
	}
	res.print(os.Stdout)
	if f := res.failures(o.assertP95, assertRSS); len(f) > 0 {
		return errors.New("assertion failed: " + strings.Join(f, "; "))
	}
	fmt.Println("PASS")
	return nil
}

// privateServer is a bubbles child process on a throwaway data directory.
type privateServer struct {
	cmd     *exec.Cmd
	base    string
	pool    *x509.CertPool
	pid     int
	dataDir string
	keep    string
	cleanup func()
}

func (s *privateServer) stop() {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
	}
	s.cleanup()
}

func freeAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return l.Addr().String(), nil
}

func startServer(ctx context.Context, o *options) (*privateServer, error) {
	root := o.keepDir
	if root == "" {
		d, err := os.MkdirTemp("", "bubbles-loadtest-")
		if err != nil {
			return nil, err
		}
		root = d
	} else if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	s := &privateServer{dataDir: filepath.Join(root, "data")}
	if o.keepDir != "" {
		s.keep = root
	}
	s.cleanup = func() {
		if o.keepDir == "" {
			_ = os.RemoveAll(root)
		}
	}
	fail := func(err error) (*privateServer, error) {
		s.cleanup()
		return nil, err
	}

	exe := filepath.Join(root, "bubbles")
	if isWindows() {
		exe += ".exe"
	}
	modRoot, err := moduleRoot()
	if err != nil {
		return fail(err)
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", exe, "./cmd/bubbles")
	build.Dir = modRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		return fail(fmt.Errorf("go build: %w\n%s", err, out))
	}
	httpsAddr, err := freeAddr()
	if err != nil {
		return fail(err)
	}
	httpAddr, err := freeAddr()
	if err != nil {
		return fail(err)
	}
	logFile, err := os.Create(filepath.Join(root, "server.log"))
	if err != nil {
		return fail(err)
	}
	args := []string{"serve", "--data-dir", s.dataDir, "--https-addr", httpsAddr, "--http-addr", httpAddr, "--no-mdns", "--allow-public"}
	if o.serverArgs != "" {
		args = append(args, strings.Fields(o.serverArgs)...)
	}
	s.cmd = exec.Command(exe, args...)
	s.cmd.Stdout, s.cmd.Stderr = logFile, logFile
	if err := s.cmd.Start(); err != nil {
		return fail(err)
	}
	s.pid = s.cmd.Process.Pid
	s.base = "https://" + httpsAddr

	probe := newAPI(s.base, nil, true)
	deadline := time.Now().Add(60 * time.Second)
	for {
		status, _, _, err := probe.do(ctx, "GET", "/healthz", "", nil)
		if err == nil && status == 200 {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			s.stop()
			return nil, errors.New("server did not become healthy")
		}
		time.Sleep(200 * time.Millisecond)
	}
	pem, err := os.ReadFile(filepath.Join(s.dataDir, "tls", "ca.crt"))
	if err != nil {
		s.stop()
		return nil, fmt.Errorf("read CA: %w", err)
	}
	s.pool = x509.NewCertPool()
	if !s.pool.AppendCertsFromPEM(pem) {
		s.stop()
		return nil, errors.New("CA file has no certificate")
	}
	return s, nil
}

// setup completes first-run setup and fills admin with the session.
func (s *privateServer) setup(ctx context.Context, admin *user) error {
	token, err := os.ReadFile(filepath.Join(s.dataDir, "setup-token.txt"))
	if err != nil {
		return err
	}
	admin.name, admin.password = "loadadmin", randomPassword()
	a := newAPI(s.base, s.pool, false)
	set, err := a.call(ctx, "POST", "/api/v1/setup", "", protocol.SetupRequest{
		SetupToken:   strings.TrimSpace(string(token)),
		Username:     admin.name,
		Password:     admin.password,
		DisplayName:  "Load Admin",
		InstanceName: "loadtest",
	}, nil, 201)
	if err != nil {
		return err
	}
	admin.cookie = set
	return nil
}

// populate creates the channels and users (each joined to one channel).
func populate(ctx context.Context, a *api, admin *user, o *options) ([]*user, []string, error) {
	chanIDs := make([]string, o.channels)
	prefix := fmt.Sprintf("lt%d", time.Now().Unix()%100000)
	for i := range chanIDs {
		var ch protocol.Channel
		if _, err := a.call(ctx, "POST", "/api/v1/channels", admin.cookie,
			protocol.CreateChannelRequest{Name: fmt.Sprintf("%s-ch-%02d", prefix, i), Kind: "public"}, &ch, 201); err != nil {
			return nil, nil, err
		}
		chanIDs[i] = ch.ID
	}
	var inv struct {
		URL string `json:"url"`
	}
	if _, err := a.call(ctx, "POST", "/api/v1/admin/invites", admin.cookie,
		map[string]any{"role": "member", "max_uses": o.clients, "expires_in_seconds": 3600}, &inv, 201); err != nil {
		return nil, nil, err
	}
	code := inv.URL[strings.LastIndex(inv.URL, "/")+1:]

	users := make([]*user, o.clients)
	var (
		wg    sync.WaitGroup
		sem   = make(chan struct{}, 8) // argon2 is memory-hungry
		first atomic.Value
	)
	for i := range users {
		users[i] = &user{name: fmt.Sprintf("%s-u%03d", prefix, i), password: randomPassword(), channel: i % o.channels}
		wg.Add(1)
		go func(u *user) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var out protocol.AuthResponse
			set, err := a.call(ctx, "POST", "/api/v1/invites/"+code+"/accept", "",
				protocol.AcceptInviteRequest{Username: u.name, Password: u.password, DisplayName: u.name}, &out, 201)
			if err == nil {
				u.cookie, u.id = set, out.User.ID
				var status int
				status, _, _, err = a.do(ctx, "POST", "/api/v1/channels/"+chanIDs[u.channel]+"/join", u.cookie, nil)
				if err == nil && status/100 != 2 {
					err = fmt.Errorf("join: status %d", status)
				}
			}
			if err != nil {
				first.CompareAndSwap(nil, err)
			}
		}(users[i])
	}
	wg.Wait()
	if e := first.Load(); e != nil {
		return nil, nil, e.(error)
	}
	return users, chanIDs, nil
}

// drive opens the sockets, runs the senders and evaluates what arrived.
func drive(ctx context.Context, a *api, users []*user, chanIDs []string, o *options) (result, error) {
	res := result{Clients: len(users), Senders: o.senders, Channels: len(chanIDs), Duration: o.duration}
	clients := make([]*ws, len(users))
	var wg sync.WaitGroup
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	for i, u := range users {
		c, err := a.dialWS(ctx, u)
		if err != nil {
			return res, fmt.Errorf("client %d: %w", i, err)
		}
		clients[i] = c
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.run(runCtx)
		}()
	}
	defer func() {
		for _, c := range clients {
			_ = c.conn.CloseNow()
		}
		cancelRun()
		wg.Wait()
	}()
	for i, c := range clients {
		select {
		case <-c.ready:
		case <-time.After(30 * time.Second):
			return res, fmt.Errorf("client %d: no hello", i)
		case <-ctx.Done():
			return res, ctx.Err()
		}
	}
	fmt.Printf("%d clients connected\n", len(clients))

	var (
		mu        sync.Mutex
		sentIDs   = make([]map[string]struct{}, len(chanIDs))
		sent      atomic.Int64
		sendErrs  atomic.Int64
		sendWG    sync.WaitGroup
		sendCtx   context.Context
		cancelSnd context.CancelFunc
	)
	for i := range sentIDs {
		sentIDs[i] = map[string]struct{}{}
	}
	sendCtx, cancelSnd = context.WithTimeout(ctx, o.duration)
	defer cancelSnd()
	interval := time.Duration(float64(time.Second) / o.rate)
	for i := 0; i < o.senders; i++ {
		u := users[i]
		sendWG.Add(1)
		go func(idx int) {
			defer sendWG.Done()
			// Spread the first send so senders do not fire in lockstep.
			select {
			case <-time.After(interval * time.Duration(idx) / time.Duration(o.senders+1)):
			case <-sendCtx.Done():
				return
			}
			t := time.NewTicker(interval)
			defer t.Stop()
			for n := 0; ; n++ {
				body := fmt.Sprintf("%s%d|%d|%d", bodyPrefix, time.Now().UnixNano(), idx, n)
				var m protocol.Message
				_, err := a.call(context.WithoutCancel(sendCtx), "POST", "/api/v1/channels/"+chanIDs[u.channel]+"/messages", u.cookie,
					protocol.CreateMessageRequest{Body: body}, &m, 201)
				if err != nil {
					sendErrs.Add(1)
				} else {
					sent.Add(1)
					mu.Lock()
					sentIDs[u.channel][m.ID] = struct{}{}
					mu.Unlock()
				}
				select {
				case <-t.C:
				case <-sendCtx.Done():
					return
				}
			}
		}(i)
	}
	sendWG.Wait()

	// Drain: wait until every client has everything posted to its channel.
	drainBy := time.Now().Add(15 * time.Second)
	for time.Now().Before(drainBy) {
		if incomplete(clients, sentIDs, &mu) == 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	var lat []time.Duration
	mu.Lock()
	defer mu.Unlock()
	res.Sent, res.SendErrors = sent.Load(), sendErrs.Load()
	for _, c := range clients {
		c.mu.Lock()
		want := sentIDs[c.u.channel]
		missing := 0
		for id := range want {
			if _, ok := c.got[id]; !ok {
				missing++
			}
		}
		if missing > 0 {
			res.ClientsIncomplete++
		}
		res.Lost += int64(missing)
		res.Expected += int64(len(want))
		res.Received += int64(len(c.got))
		res.Duplicates += c.dups
		res.SeqRegressions += c.seqRegress
		lat = append(lat, c.latencies...)
		c.mu.Unlock()
	}
	res.P50, res.P95, res.P99, res.Max = summarize(lat)
	return res, nil
}

func incomplete(clients []*ws, sentIDs []map[string]struct{}, mu *sync.Mutex) int {
	mu.Lock()
	defer mu.Unlock()
	n := 0
	for _, c := range clients {
		c.mu.Lock()
		if len(c.got) < len(sentIDs[c.u.channel]) {
			n++
		}
		c.mu.Unlock()
	}
	return n
}

func isWindows() bool { return os.PathSeparator == '\\' }

// moduleRoot finds the directory holding go.mod, starting at the working
// directory.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found; run from inside the repository")
		}
		dir = parent
	}
}
