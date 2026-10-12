// Package app is the composition root of `bubbles serve`: it opens the
// store, loads the TLS material, builds the HTTPS and onboarding handlers,
// binds both listeners, runs the background jobs and shuts everything down
// gracefully.
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/donaldsteele/bubbles/internal/alerts"
	"github.com/donaldsteele/bubbles/internal/apps"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/buildinfo"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/discovery"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/internal/files"
	"github.com/donaldsteele/bubbles/internal/hooks"
	"github.com/donaldsteele/bubbles/internal/httpapi"
	"github.com/donaldsteele/bubbles/internal/netutil"
	"github.com/donaldsteele/bubbles/internal/notify"
	"github.com/donaldsteele/bubbles/internal/onboarding"
	"github.com/donaldsteele/bubbles/internal/push"
	"github.com/donaldsteele/bubbles/internal/reset"
	"github.com/donaldsteele/bubbles/internal/retention"
	"github.com/donaldsteele/bubbles/internal/search"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/internal/tlsca"
	"github.com/donaldsteele/bubbles/internal/wsgateway"
)

// DefaultShutdownTimeout bounds the graceful drain on shutdown.
const DefaultShutdownTimeout = 10 * time.Second

// Options are the injectable dependencies of App. The zero value uses the
// real clock, tickers, network interfaces and host name.
type Options struct {
	// Stderr receives the startup banner; nil discards it.
	Stderr io.Writer
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// NewTicker creates job tickers; nil means time.NewTicker.
	NewTicker func(time.Duration) Ticker
	// LANAddrs returns the ranked private LAN addresses; nil means
	// DefaultLANAddrs.
	LANAddrs func() ([]netip.Addr, error)
	// Hostname returns the OS host name; nil means os.Hostname.
	Hostname func() (string, error)
	// ShutdownTimeout bounds the graceful drain; 0 means DefaultShutdownTimeout.
	ShutdownTimeout time.Duration
	// AuthParams are the argon2id parameters for new password hashes; the
	// zero value means auth.DefaultParams (tests pass cheap ones).
	AuthParams auth.Params
	// BackupWorkFactor is the scrypt work factor (log2 N) for admin backups;
	// 0 means age's default (tests pass a cheap one).
	BackupWorkFactor int
	// AdminPassword fixes the password of the bootstrapped admin instead of a
	// generated one (tests).
	AdminPassword string
	// LogFiles is the writer behind the server log; a factory reset wipes
	// the log directory through it. Nil: the directory is wiped directly.
	LogFiles LogFiles
}

// LogFiles is a log file writer a factory reset can wipe while the server
// runs (logging.RotatingWriter).
type LogFiles interface {
	// Reopen closes the live file, runs clear, then opens a fresh file.
	Reopen(clear func() error) error
}

func (o Options) withDefaults() Options {
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewTicker == nil {
		o.NewTicker = newRealTicker
	}
	if o.LANAddrs == nil {
		o.LANAddrs = DefaultLANAddrs
	}
	if o.Hostname == nil {
		o.Hostname = os.Hostname
	}
	if o.ShutdownTimeout <= 0 {
		o.ShutdownTimeout = DefaultShutdownTimeout
	}
	return o
}

// DefaultLANAddrs returns the host's ranked private LAN addresses.
func DefaultLANAddrs() ([]netip.Addr, error) {
	cands, err := netutil.Candidates()
	if err != nil {
		return nil, err
	}
	return netutil.Addrs(cands), nil
}

// App is a running bubbles server.
type App struct {
	cfg    config.Config
	logger *slog.Logger
	opts   Options

	// startedAt is when New returned; the admin overview reports uptime.
	startedAt time.Time

	db       *store.DB
	hub      *events.Hub
	ws       *wsgateway.Gateway
	settings *settings.Store
	auth     *auth.Service
	chat     *chat.Service
	files    *files.Service
	apps     *apps.Service
	alerts   *alerts.Service
	hooks    *hooks.Service
	push     *push.Service
	notify   *notify.Dispatcher
	retain   *retention.Job
	certs    atomic.Pointer[tlsca.Manager] // replaced when a factory reset rotates the CA
	httpsLn  net.Listener
	httpLn   net.Listener
	httpsSrv *http.Server
	httpSrv  *http.Server
	jobs     []Job

	// isolationDone is set once the client-isolation check has concluded.
	isolationDone atomic.Bool

	// The listeners serve whatever handler the slots hold, behind the
	// maintenance gate; a factory reset rebuilds the services and swaps the
	// handlers.
	gate        *maintenance
	apiSlot     handlerSlot
	onboardSlot handlerSlot

	// lifeMu serializes the lifecycle: background start/stop, reset and
	// shutdown. The service fields above are written only under it, while
	// the jobs are stopped and the gate refuses requests.
	lifeMu    sync.Mutex
	running   bool               // Run is serving: background work belongs on
	bgCancel  context.CancelFunc // stops the jobs and mDNS of startBackground
	jobsWG    sync.WaitGroup
	closed    bool
	caRotated bool // a reset replaced the CA: re-announce mDNS after rebuild
	resetMu   sync.Mutex

	// life bounds the background trash sweeps; Shutdown cancels it.
	life     context.Context
	lifeStop context.CancelFunc
	trashWG  sync.WaitGroup

	// adminPassword is the plaintext password of a freshly bootstrapped
	// admin, held only until the startup banner (or the factory-reset
	// notice) has printed it.
	adminPassword string

	mdnsMu        sync.Mutex
	mdns          *discovery.Advertiser
	mdnsWG        sync.WaitGroup
	mdnsRefreshes atomic.Int64

	shutdownOnce sync.Once
	shutdownErr  error
}

// New wires the server for cfg. The data directory must have been prepared
// with config.EnsureDataDir and locked by the caller. Both listeners are
// bound before New returns (see HTTPSAddr and HTTPAddr), but nothing is
// served until Run.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger, opts Options) (_ *App, err error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	a := &App{cfg: cfg, logger: logger, opts: opts.withDefaults(), gate: &maintenance{}}
	a.startedAt = a.opts.Now()
	a.life, a.lifeStop = context.WithCancel(context.Background())
	defer func() {
		if err != nil {
			a.lifeStop()
			a.closeResources()
		}
	}()

	// A reset cut short by a crash or power loss is completed before
	// anything opens the database or a listener.
	recovered, err := reset.Recover(ctx, cfg.DataDir, reset.RecoverOptions{
		ReopenLogs: a.reopenLogs(), Now: a.opts.Now, Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("complete the interrupted reset: %w", err)
	}
	if recovered != nil {
		logger.Warn("completed an interrupted reset", "mode", string(recovered.Mode))
	}
	certs, err := CertManager(cfg, logger, a.opts)
	if err != nil {
		return nil, err
	}
	a.certs.Store(certs)
	a.httpsLn, err = net.Listen("tcp", cfg.HTTPSAddr)
	if err != nil {
		return nil, fmt.Errorf("listen on --https-addr %s: %w", cfg.HTTPSAddr, err)
	}
	a.httpLn, err = net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return nil, fmt.Errorf("listen on --http-addr %s: %w", cfg.HTTPAddr, err)
	}
	if err := a.openCore(ctx); err != nil {
		return nil, err
	}
	getCert := func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) { return a.Certs().GetCertificate(hello) }
	a.httpsSrv = httpapi.NewServer(a.gate.wrap(&a.apiSlot), getCert, logger)
	a.httpSrv = onboarding.NewServer(a.gate.wrap(&a.onboardSlot), logger)
	return a, nil
}

// openCore opens the database and builds every service, both HTTP handlers
// and the job list on it. New calls it once; a factory reset calls it again
// on the recreated database (with the jobs stopped and the gate closed).
func (a *App) openCore(ctx context.Context) (err error) {
	cfg, logger := a.cfg, a.logger
	a.db, err = store.Open(ctx, filepath.Join(cfg.DataDir, store.FileName), store.Options{Now: a.opts.Now, Logger: logger})
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	// Every event appended from here on is published to the hub after its
	// transaction commits.
	a.hub = events.NewHub(events.HubOptions{DB: a.db, Now: a.opts.Now, Logger: logger})
	a.ws = wsgateway.New(wsgateway.Options{Hub: a.hub, DB: a.db, Version: buildinfo.Version, Logger: logger})

	a.settings = settings.New(a.db)
	a.chat = chat.New(a.db, a.settings)
	a.files, err = files.New(files.Options{DB: a.db, Settings: a.settings, DataDir: cfg.DataDir, Logger: logger})
	if err != nil {
		return err
	}
	a.chat.SetFileAttacher(a.files)
	a.apps = apps.New(apps.Options{DB: a.db, Chat: a.chat, Now: a.opts.Now, Logger: logger, Realtime: a.hub})
	a.alerts = alerts.New(alerts.Options{DB: a.db, Chat: a.chat, Settings: a.settings})
	a.hooks = hooks.New(hooks.Options{DB: a.db, Chat: a.chat, Apps: a.apps, Alerts: a.alerts, Now: a.opts.Now})
	// Web Push: the VAPID key pair is created on first run; the dispatcher
	// sees every committed event next to the hub and pushes to members who
	// have no window in focus.
	a.push, err = push.New(push.Options{DB: a.db, Settings: a.settings, DataDir: cfg.DataDir, Logger: logger})
	if err != nil {
		return err
	}
	a.notify = notify.New(notify.Options{
		DB: a.db, Settings: a.settings, Presence: a.hub, Pusher: a.push, Now: a.opts.Now, Logger: logger,
	})
	a.notify.Start()
	events.SetPublisher(a.db, events.NewMultiPublisher(a.hub, a.notify))
	// Uploads cut off by a restart left spool files behind; nothing is in
	// flight yet, so all of them are stale.
	if n, err := a.files.SweepTmp(0); err != nil {
		logger.Warn("sweep upload spool", "err", err)
	} else if n > 0 {
		logger.Info("removed stale upload spool files", "count", n)
	}
	a.retain = retention.New(retention.Options{
		DB: a.db, Settings: a.settings, Blobs: a.files, Now: a.opts.Now, Logger: logger,
	})
	a.auth, err = auth.New(auth.Options{
		DB: a.db, Settings: a.settings, DataDir: cfg.DataDir, Now: a.opts.Now, Logger: logger,
		Params: a.opts.AuthParams,
		// #general is created in the bootstrap transaction; every new member
		// or admin joins the default channels in the transaction that creates
		// them.
		OnBootstrap: a.chat.OnBootstrapTx, OnUserCreated: a.chat.OnUserCreatedTx,
		AdminPassword: a.bootstrapPassword(),
	})
	if err != nil {
		return err
	}
	// With no users yet this creates the admin and writes
	// <data>/admin-password.txt; the banner prints the password once the
	// server is up. Right after a factory reset the reset notice does.
	a.adminPassword, err = a.auth.BootstrapAdmin(ctx)
	if err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}

	api := httpapi.New(httpapi.Deps{
		Version:      buildinfo.Version,
		Logger:       logger,
		AllowedCIDRs: a.settings.AllowedCIDRs,
		Auth:         a.auth,
		Chat:         a.chat,
		Files:        a.files,
		Search:       search.New(a.db, a.chat, nil),
		Push:         a.push,
		Apps:         a.apps,
		Alerts:       a.alerts,
		Hooks:        a.hooks,
		Endpoint:     a.endpoint,
		Now:          a.opts.Now,
		WS:           a.ws,
		Reset:        &resetAPI{app: a, db: a.db, settings: a.settings, auth: a.auth},
		Admin:        adminHost{app: a},
		DB:           a.db,
	})
	a.apiSlot.set(api)
	a.onboardSlot.set(onboarding.New(onboarding.Options{
		InstanceName:     a.settings.InstanceName,
		CA:               func() *tlsca.CA { return a.Certs().CA() },
		HTTPSPort:        a.HTTPSPort(),
		URLs:             a.URLs,
		Hosts:            func() []string { return a.Certs().Status().SANs },
		AllowedCIDRs:     a.settings.AllowedCIDRs,
		RegistrationOpen: func() bool { return a.settings.RegistrationOpen(a.opts.Now()) },
		Logger:           logger,
	}))
	a.jobs = a.defaultJobs()
	return nil
}

// CertManager builds the TLS certificate manager for cfg: bring-your-own
// files when --tls-cert/--tls-key are set, else the local CA in
// <data>/tls with the leaf SANs discovered from the LAN addresses, host
// name, mDNS name and --tls-extra-dns. `bubbles cert reissue` uses it too.
func CertManager(cfg config.Config, logger *slog.Logger, opts Options) (*tlsca.Manager, error) {
	opts = opts.withDefaults()
	host, _ := opts.Hostname()
	return tlsca.NewManager(tlsca.ManagerOptions{
		Dir:      filepath.Join(cfg.DataDir, config.DirTLS),
		CertFile: cfg.TLSCert,
		KeyFile:  cfg.TLSKey,
		CA:       tlsca.CAOptions{InstanceName: tlsca.NormalizeHostname(host), Hostname: host, ExtraDNS: cfg.TLSExtraDNS},
		SANs:     SANSource(cfg, opts.LANAddrs, opts.Hostname),
		Now:      opts.Now,
		Logger:   logger,
	})
}

// SANSource returns the leaf SAN discovery function for cfg.
func SANSource(cfg config.Config, lanAddrs func() ([]netip.Addr, error), hostname func() (string, error)) func() (tlsca.SANs, error) {
	return func() (tlsca.SANs, error) {
		addrs, err := lanAddrs()
		if err != nil {
			return tlsca.SANs{}, err
		}
		host, _ := hostname()
		return tlsca.Discover(tlsca.SANInput{Addrs: addrs, Hostname: host, MDNSName: cfg.MDNSName, Extra: cfg.TLSExtraDNS}), nil
	}
}

// HTTPSAddr is the bound HTTPS listener address.
func (a *App) HTTPSAddr() net.Addr { return a.httpsLn.Addr() }

// HTTPAddr is the bound onboarding listener address.
func (a *App) HTTPAddr() net.Addr { return a.httpLn.Addr() }

// HTTPSPort is the bound HTTPS port.
func (a *App) HTTPSPort() int { return portOf(a.httpsLn.Addr()) }

// Certs is the TLS certificate manager (a factory reset that rotates the CA
// replaces it).
func (a *App) Certs() *tlsca.Manager { return a.certs.Load() }

func portOf(addr net.Addr) int {
	if tcp, ok := addr.(*net.TCPAddr); ok {
		return tcp.Port
	}
	_, p, _ := net.SplitHostPort(addr.String())
	n, _ := strconv.Atoi(p)
	return n
}

// urlHosts returns the hosts clients should use for ln: its own address
// when bound to one, else the ranked LAN addresses (IPv6 link-local ones are
// skipped: browsers cannot use them without a zone), else 127.0.0.1.
func (a *App) urlHosts(ln net.Listener) []string {
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok && !tcp.IP.IsUnspecified() {
		if ip, ok := netip.AddrFromSlice(tcp.IP); ok {
			return []string{ip.Unmap().String()}
		}
	}
	addrs, err := a.opts.LANAddrs()
	if err != nil {
		a.logger.Warn("list LAN addresses", "err", err)
	}
	var hosts []string
	for _, ip := range addrs {
		if ip.Is6() && ip.IsLinkLocalUnicast() {
			continue
		}
		hosts = append(hosts, ip.Unmap().WithZone("").String())
	}
	if len(hosts) == 0 {
		hosts = []string{"127.0.0.1"}
	}
	return hosts
}

func baseURLs(scheme string, hosts []string, port int) []string {
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, scheme+"://"+net.JoinHostPort(h, strconv.Itoa(port))+"/")
	}
	return out
}

// endpoint says how an app reaches this server: the first LAN host, the HTTPS
// port and the fingerprint it pins (the CA's, or the served certificate's
// with --tls-cert).
func (a *App) endpoint() apps.Endpoint {
	st := a.Certs().Status()
	fp := st.CAFingerprint
	if st.Mode == tlsca.ModeBYO {
		fp = st.LeafFingerprint
	}
	return apps.Endpoint{Host: a.urlHosts(a.httpsLn)[0], Port: a.HTTPSPort(), Fingerprint: fp}
}

// URLs returns the HTTPS base URLs of this server, best first.
func (a *App) URLs() []string {
	return baseURLs("https", a.urlHosts(a.httpsLn), a.HTTPSPort())
}

// OnboardingURLs returns the onboarding base URLs, best first.
func (a *App) OnboardingURLs() []string {
	return baseURLs("http", a.urlHosts(a.httpLn), portOf(a.httpLn.Addr()))
}

// Run serves both listeners and runs the jobs until ctx is done or a
// listener fails, then shuts down gracefully within the shutdown timeout.
func (a *App) Run(ctx context.Context) error {
	errc := make(chan error, 2)
	go func() { errc <- serveErr("https", a.httpsSrv.ServeTLS(a.httpsLn, "", "")) }()
	go func() { errc <- serveErr("http", a.httpSrv.Serve(a.httpLn)) }()

	a.lifeMu.Lock()
	a.running = true
	a.startBackground()
	a.logger.Info("listening", "https", a.HTTPSAddr().String(), "http", a.HTTPAddr().String(), "tls_mode", string(a.Certs().Mode()))
	a.printBanner(a.opts.Stderr)
	a.lifeMu.Unlock()

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errc:
	}
	a.lifeMu.Lock()
	a.running = false
	a.stopBackground()
	a.lifeMu.Unlock()

	sctx, cancel := context.WithTimeout(context.Background(), a.opts.ShutdownTimeout)
	defer cancel()
	if err := a.Shutdown(sctx); err != nil && runErr == nil {
		runErr = err
	}
	a.logger.Info("stopped")
	return runErr
}

func serveErr(name string, err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("%s listener: %w", name, err)
}

// Shutdown stops accepting connections, drains in-flight requests until ctx
// expires (then closes them), and closes the database. It is idempotent.
func (a *App) Shutdown(ctx context.Context) error {
	a.shutdownOnce.Do(func() {
		var errs []error
		var wg sync.WaitGroup
		for _, srv := range []*http.Server{a.httpsSrv, a.httpSrv} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := srv.Shutdown(ctx); err != nil {
					_ = srv.Close()
				}
			}()
		}
		wg.Wait()
		// A reset in flight finishes first (srv.Shutdown waited for its
		// request until ctx expired; the lock waits for the rest).
		a.lifeMu.Lock()
		defer a.lifeMu.Unlock()
		a.closed = true
		a.stopBackground()
		// Hijacked WebSockets outlive srv.Shutdown: close them before the
		// hub and the database go away.
		if a.ws != nil {
			a.ws.CloseAll(int(websocket.StatusGoingAway), "server shutting down")
		}
		a.lifeStop()
		a.trashWG.Wait()
		if err := a.closeResources(); err != nil {
			errs = append(errs, err)
		}
		a.shutdownErr = errors.Join(errs...)
	})
	return a.shutdownErr
}

// closeResources closes the listeners (Serve may never have run) and the
// database.
func (a *App) closeResources() error {
	for _, ln := range []net.Listener{a.httpsLn, a.httpLn} {
		if ln != nil {
			_ = ln.Close()
		}
	}
	if a.notify != nil {
		a.notify.Close()
	}
	if a.push != nil {
		a.push.Close()
	}
	if a.hub != nil {
		a.hub.Close()
	}
	if a.db != nil {
		if err := a.db.Close(); err != nil {
			return fmt.Errorf("close database: %w", err)
		}
	}
	return nil
}

// printBanner tells the console where to connect and how to verify the CA.
func (a *App) printBanner(w io.Writer) {
	fmt.Fprintf(w, "\nbubbles %s is running\n\n", buildinfo.Version)
	for i, u := range a.URLs() {
		label := ""
		if i == 0 {
			label = "Open:"
		}
		fmt.Fprintf(w, "  %-12s %s\n", label, u)
	}
	for i, u := range a.OnboardingURLs() {
		label := ""
		if i == 0 {
			label = "Onboarding:"
		}
		fmt.Fprintf(w, "  %-12s %s\n", label, u)
	}
	st := a.Certs().Status()
	if st.Mode == tlsca.ModeBYO {
		fmt.Fprintf(w, "\n  TLS certificate from --tls-cert, SHA-256 fingerprint:\n    %s\n\n", st.LeafFingerprint)
	} else {
		fmt.Fprintf(w, "\n  CA fingerprint (SHA-256), compare it on each device:\n    %s\n\n", st.CAFingerprint)
	}
	a.printAdmin(w)
}

// printAdmin shows the generated admin password (on the console only, never
// in the log). The plaintext is dropped afterwards; admin-password.txt in
// the data directory keeps it until the password changes, and a later start
// only points to that file.
func (a *App) printAdmin(w io.Writer) {
	path := filepath.Join(a.cfg.DataDir, auth.AdminPasswordFile)
	if a.adminPassword != "" {
		fmt.Fprintf(w, "  Sign in as:   %s\n  Password:     %s\n", auth.BootstrapAdminUsername, a.adminPassword)
		fmt.Fprintf(w, "  (also saved in %s; deleted when you change the password)\n\n", path)
		a.adminPassword = ""
		return
	}
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(w, "  The first admin password is in %s\n\n", path)
	}
}

// startMDNS advertises the server over mDNS unless --no-mdns or the
// mdns_enabled setting disables it. Probing can take seconds, so it runs in
// the background; failures are logged, never fatal.
func (a *App) startMDNS(ctx context.Context) {
	if a.cfg.NoMDNS || !a.settings.Values().MDNSEnabled {
		return
	}
	var cader func() []byte
	if a.Certs().CA() != nil {
		// Read at every announcement, so a rotated CA is picked up.
		cader = func() []byte {
			if ca := a.Certs().CA(); ca != nil {
				return ca.DER()
			}
			return nil
		}
	}
	adv := discovery.New(discovery.Options{
		Instance:  a.settings.InstanceName(),
		Host:      a.cfg.MDNSName,
		HTTPSPort: a.HTTPSPort(),
		HTTPPort:  portOf(a.httpLn.Addr()),
		CADER:     cader,
		OnRename: func() {
			if _, err := a.Certs().Check(); err != nil {
				a.logger.Warn("tls check after mdns rename failed", "error", err.Error())
			}
		},
		Logger: a.logger,
	})
	a.mdnsMu.Lock()
	a.mdns = adv
	a.mdnsMu.Unlock()
	a.mdnsWG.Add(1)
	go func() {
		defer a.mdnsWG.Done()
		if err := adv.Start(ctx); err != nil && ctx.Err() == nil {
			a.logger.Warn("mdns advertise failed", "error", err.Error())
		}
	}()
}

// RefreshMDNS re-announces the TXT record after the CA fingerprint changed.
func (a *App) RefreshMDNS() {
	a.mdnsRefreshes.Add(1)
	a.mdnsMu.Lock()
	adv := a.mdns
	a.mdnsMu.Unlock()
	if adv != nil {
		adv.Refresh()
	}
}

// stopMDNS waits for the probe to end (its context is cancelled first) and
// closes the advertiser.
func (a *App) stopMDNS() {
	a.mdnsWG.Wait()
	a.mdnsMu.Lock()
	adv := a.mdns
	a.mdns = nil
	a.mdnsMu.Unlock()
	if adv != nil {
		adv.Close()
	}
}
