package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/donaldsteele/bubbles/pkg/bubblesclient"
	"github.com/donaldsteele/bubbles/tray/internal/autostart"
	"github.com/donaldsteele/bubbles/tray/internal/core"
)

const userAgent = "bubbles-tray/1"

// Window names.
const (
	loginWindow = "login"
	mainWindow  = "main"
)

// flow is one pass through the sign-in screens.
type flow struct {
	screen        string // "server", "confirm" or "login"
	notice        string
	serverURL     string
	onboardingURL string
	port          string
	der           []byte
	prompt        *core.Prompt
	trust         *core.Trust
	// reconfirm: the user is already signed in and the pinned CA no longer
	// matches; confirming re-pins and reconnects instead of signing in again.
	reconfirm bool
}

// trayApp is the tray application: it owns the config, the connection
// monitor, the sign-in flow and the windows. Wails calls are never made
// while mu is held (they run on the main thread and may call back in).
type trayApp struct {
	app      *application.App
	notifier *notifier
	store    core.ConfigStore
	tokens   core.TokenStore
	dataDir  string
	logger   *slog.Logger
	exe      string

	ui *trayUI

	mu       sync.Mutex
	cfg      core.Config
	trust    *core.Trust
	engine   *core.Engine
	client   *bubblesclient.Client
	cancel   context.CancelFunc
	status   core.Status
	flow     *flow
	signedIn bool
	loginWin *application.WebviewWindow
	mainWin  *application.WebviewWindow
}

func newTrayApp(logger *slog.Logger, dataDir string, tokens core.TokenStore, n *notifier) (*trayApp, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate executable: %w", err)
	}
	a := &trayApp{
		notifier: n, tokens: tokens, dataDir: dataDir, logger: logger, exe: exe,
		store:  core.ConfigStore{Path: dataDir + string(os.PathSeparator) + "config.json"},
		engine: core.NewEngine(),
	}
	cfg, err := a.store.Load()
	if err != nil {
		logger.Warn("ignoring unreadable config", "err", err)
		cfg = core.Config{}
	}
	a.cfg = cfg
	a.trust, err = core.NewTrust(cfg.CAFingerprint)
	if err != nil {
		a.trust, _ = core.NewTrust("")
		a.cfg.CAFingerprint, a.cfg.DeviceID = "", ""
	}
	return a, nil
}

// start runs once the Wails application is up.
func (a *trayApp) start(background bool) {
	a.notifier.authorize()
	a.notifier.onClick(func(channelID string) { go a.openBubbles(channelID) })

	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()

	if cfg.SignedIn() {
		token, err := a.tokens.Get(cfg.ServerURL)
		if err == nil && token != "" {
			a.markSignedIn(true)
			a.startMonitor(token)
			return
		}
		a.logger.Warn("device token missing from the keychain; signing in again")
		a.forgetDevice()
	}
	a.markSignedIn(false)
	a.setStatusText("Signed out")
	if !background {
		a.beginSignIn("")
	}
}

func (a *trayApp) markSignedIn(v bool) {
	a.mu.Lock()
	a.signedIn = v
	a.mu.Unlock()
	a.ui.setSignedIn(v)
}

// ---- monitor ---------------------------------------------------------------

func (a *trayApp) startMonitor(token string) {
	a.stopMonitor()
	a.mu.Lock()
	cfg := a.cfg
	a.engine = core.NewEngine()
	a.engine.SetPausedUntil(cfg.PausedUntil)
	engine := a.engine
	a.mu.Unlock()

	client, err := bubblesclient.New(bubblesclient.Config{
		BaseURL: cfg.ServerURL, Token: token, CAFingerprint: cfg.CAFingerprint, UserAgent: userAgent,
	})
	if err != nil {
		a.logger.Error("cannot build the client", "err", err)
		a.setStatusText("Configuration error")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.client, a.cancel = client, cancel
	a.mu.Unlock()
	mon := &core.Monitor{Backend: client, Engine: engine, Sink: a}
	go func() {
		out := mon.Run(ctx)
		if ctx.Err() != nil {
			return
		}
		switch out {
		case core.OutcomeSessionEnded:
			a.sessionEnded()
		case core.OutcomeIdentityChanged:
			a.identityChanged()
		}
	}()
}

func (a *trayApp) stopMonitor() {
	a.mu.Lock()
	cancel := a.cancel
	a.cancel, a.client = nil, nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Notify implements core.Sink.
func (a *trayApp) Notify(n core.Notification) {
	if n.Silent {
		return
	}
	a.notifier.show(n.ID, n.Title, n.Body, n.ChannelID, n.Critical)
}

// Indicator implements core.Sink.
func (a *trayApp) Indicator(ind core.Indicator) { a.ui.setIndicator(ind) }

// Status implements core.Sink.
func (a *trayApp) Status(s core.Status) {
	a.mu.Lock()
	a.status = s
	host := hostOf(a.cfg.ServerURL)
	a.mu.Unlock()
	switch s {
	case core.StatusOnline:
		a.setStatusText("Connected to " + host)
	case core.StatusOffline:
		a.setStatusText("Offline, retrying")
	default:
		a.setStatusText("Connecting to " + host)
	}
}

func (a *trayApp) setStatusText(s string) { a.ui.setStatus(s) }

func hostOf(serverURL string) string {
	s := strings.TrimPrefix(serverURL, "https://")
	return s
}

// ---- reset and identity handling -------------------------------------------

// sessionEnded handles a 401 or a 4009/4010 close: the device token is void
// (revoked, or the server was reset). The token is forgotten, the user is
// told, and the sign-in window opens.
func (a *trayApp) sessionEnded() {
	a.stopMonitor()
	a.forgetDevice()
	a.markSignedIn(false)
	a.setStatusText("Signed out")
	a.ui.setIndicator(core.IndicatorNormal)
	a.notifier.show("", "bubbles", "Server was reset or this device was signed out. Sign in again.", "", false)
	go a.beginSignIn("The server was reset or this device was signed out. Sign in again.")
}

// identityChanged handles ErrServerIdentityChanged: the certificate no longer
// chains to the pinned CA. Nothing is re-trusted silently; the user compares
// the new fingerprint first. The token is not sent anywhere meanwhile.
func (a *trayApp) identityChanged() {
	a.stopMonitor()
	a.mu.Lock()
	a.trust.IdentityChanged()
	cfg := a.cfg
	a.mu.Unlock()
	a.setStatusText("Server identity changed")
	a.notifier.show("", "bubbles: server identity changed",
		"The server certificate no longer matches the one you trusted. Open bubbles to review the new fingerprint.", "", true)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		st := a.prepare(ctx, cfg.ServerURL, portOf(cfg.OnboardingURL), true)
		if st.Screen == "server" {
			a.setFlowNotice("The server identity changed and its certificate could not be downloaded. Check the server, then try again.")
		} else {
			a.setFlowNotice("The server identity changed. Compare the new fingerprint with the one the server shows before you trust it.")
		}
		a.showLoginWindow()
	}()
}

func portOf(onboardingURL string) string {
	i := strings.LastIndex(onboardingURL, ":")
	if i < 0 || strings.Contains(onboardingURL[i:], "/") {
		return ""
	}
	return onboardingURL[i+1:]
}

// forgetDevice deletes the keychain entry and the device identity. The
// server URL and the pinned CA stay so the next sign-in starts quickly.
func (a *trayApp) forgetDevice() {
	a.mu.Lock()
	server := a.cfg.ServerURL
	a.cfg.DeviceID, a.cfg.Username = "", ""
	cfg := a.cfg
	a.mu.Unlock()
	if server != "" {
		if err := a.tokens.Delete(server); err != nil {
			a.logger.Warn("delete keychain entry failed", "err", err)
		}
	}
	if err := a.store.Save(cfg); err != nil {
		a.logger.Warn("save config failed", "err", err)
	}
}

// ---- menu actions ----------------------------------------------------------

// signOut revokes this device on the server (best effort), deletes the
// keychain entry and opens the sign-in window.
func (a *trayApp) signOut() {
	a.mu.Lock()
	client, deviceID := a.client, a.cfg.DeviceID
	a.mu.Unlock()
	revoked := true
	if client != nil && deviceID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := client.RevokeDevice(ctx, deviceID); err != nil && !errors.Is(err, bubblesclient.ErrUnauthorized) {
			revoked = false
			a.logger.Warn("revoking the device failed", "err", err)
		}
		cancel()
	}
	a.stopMonitor()
	a.forgetDevice()
	a.markSignedIn(false)
	a.setStatusText("Signed out")
	a.ui.setIndicator(core.IndicatorNormal)
	a.closeMainWindow()
	if !revoked {
		a.notifier.show("", "bubbles", "Signed out here, but the server could not be reached to revoke this device. Remove it in bubbles under Settings, Sessions & devices.", "", false)
	}
	a.beginSignIn("")
}

func (a *trayApp) pause(kind string) {
	until, err := core.PauseUntil(kind, time.Now())
	if err != nil {
		return
	}
	a.setPausedUntil(until.UnixMilli())
	time.AfterFunc(time.Until(until)+time.Second, a.ui.refreshPause)
}

func (a *trayApp) resume() { a.setPausedUntil(0) }

func (a *trayApp) setPausedUntil(ms int64) {
	a.mu.Lock()
	a.cfg.PausedUntil = ms
	cfg := a.cfg
	engine := a.engine
	a.mu.Unlock()
	engine.SetPausedUntil(ms)
	if err := a.store.Save(cfg); err != nil {
		a.logger.Warn("save config failed", "err", err)
	}
	a.ui.refreshPause()
}

func (a *trayApp) pausedUntil() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg.PausedUntil <= time.Now().UnixMilli() {
		return time.Time{}
	}
	return time.UnixMilli(a.cfg.PausedUntil)
}

func (a *trayApp) toggleAutostart() {
	on, _ := autostart.Enabled()
	var err error
	if on {
		err = autostart.Disable()
	} else {
		err = autostart.Enable(a.exe)
	}
	if err != nil {
		a.logger.Warn("start at login change failed", "err", err)
		a.notifier.show("", "bubbles", "Could not change start at login.", "", false)
	}
	a.ui.refreshAutostart()
}

// ---- windows ---------------------------------------------------------------

// openBubbles opens (or focuses) the main window. A new window loads the
// handoff URL so the web app is signed in without a second login.
func (a *trayApp) openBubbles(channelID string) {
	a.mu.Lock()
	client, win, engine, signedIn := a.client, a.mainWin, a.engine, a.signedIn
	server := a.cfg.ServerURL
	a.mu.Unlock()
	if !signedIn || client == nil {
		a.beginSignIn("")
		return
	}
	engine.MarkSeen()
	a.ui.setIndicator(core.IndicatorNormal)
	if win != nil {
		if p := core.ChannelPath(channelID); p != "" {
			win.SetURL(server + p)
		}
		win.Show()
		win.UnMinimise()
		win.Focus()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	code, err := client.HandoffCode(ctx)
	if err != nil {
		a.logger.Warn("handoff failed", "err", err)
		switch {
		case errors.Is(err, bubblesclient.ErrUnauthorized):
			a.sessionEnded()
		case errors.Is(err, bubblesclient.ErrServerIdentityChanged):
			a.identityChanged()
		default:
			a.notifier.show("", "bubbles", "Cannot reach the server.", "", false)
		}
		return
	}
	// With no window open, the handoff itself lands on the channel.
	a.showMainWindow(client.HandoffURLNext(code, core.ChannelPath(channelID)))
}

func (a *trayApp) showMainWindow(url string) {
	win := a.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name: mainWindow, Title: "bubbles", Width: 1200, Height: 800, MinWidth: 480, MinHeight: 480, URL: url,
	})
	win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
		a.mu.Lock()
		if a.mainWin == win {
			a.mainWin = nil
		}
		a.mu.Unlock()
	})
	a.mu.Lock()
	a.mainWin = win
	a.mu.Unlock()
	win.Show().Focus()
}

func (a *trayApp) closeMainWindow() {
	a.mu.Lock()
	win := a.mainWin
	a.mainWin = nil
	a.mu.Unlock()
	if win != nil {
		win.Close()
	}
}

// showLoginWindow opens the native sign-in window, or focuses it.
func (a *trayApp) showLoginWindow() {
	a.mu.Lock()
	win := a.loginWin
	a.mu.Unlock()
	if win != nil {
		win.Show()
		win.UnMinimise()
		win.Focus()
		return
	}
	win = a.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name: loginWindow, Title: "Sign in to bubbles", Width: 520, Height: 640, MinWidth: 420, MinHeight: 520,
		URL: "/", DisableResize: false,
	})
	win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
		a.mu.Lock()
		if a.loginWin == win {
			a.loginWin = nil
		}
		a.mu.Unlock()
	})
	a.mu.Lock()
	a.loginWin = win
	a.mu.Unlock()
	win.Show().Focus()
}

func (a *trayApp) closeLoginWindow() {
	a.mu.Lock()
	win := a.loginWin
	a.loginWin = nil
	a.mu.Unlock()
	if win != nil {
		win.Close()
	}
}

// beginSignIn starts a fresh sign-in flow and opens the window. When a
// server is already known and its CA still matches the pin, the flow starts
// at the password screen.
func (a *trayApp) beginSignIn(notice string) {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	if cfg.ServerURL != "" && cfg.CAFingerprint != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		st := a.prepare(ctx, cfg.ServerURL, portOf(cfg.OnboardingURL), false)
		cancel()
		if st.Screen != "server" {
			if notice != "" {
				a.setFlowNotice(notice)
			}
			a.showLoginWindow()
			return
		}
	}
	a.mu.Lock()
	a.flow = &flow{screen: "server", notice: notice, serverURL: cfg.ServerURL}
	a.mu.Unlock()
	a.showLoginWindow()
}

func (a *trayApp) setFlowNotice(n string) {
	a.mu.Lock()
	if a.flow != nil {
		a.flow.notice = n
	}
	a.mu.Unlock()
}

// currentTrust returns the trust state for server: the stored pin when it is
// the signed-in server, a fresh state otherwise.
func (a *trayApp) trustFor(serverURL string) *core.Trust {
	a.mu.Lock()
	defer a.mu.Unlock()
	if serverURL == a.cfg.ServerURL && a.cfg.CAFingerprint != "" {
		return a.trust
	}
	t, _ := core.NewTrust("")
	return t
}

func clientError(err error) string {
	var api *bubblesclient.APIError
	switch {
	case errors.As(err, &api) && api.Status == http.StatusUnauthorized:
		return "Invalid username or password."
	case errors.As(err, &api) && api.Status == http.StatusTooManyRequests:
		if api.RetryAfter > 0 {
			return fmt.Sprintf("Too many attempts. Try again in %d minutes.", int(api.RetryAfter.Minutes())+1)
		}
		return "Too many attempts. Try again later."
	case errors.As(err, &api) && len(api.FieldErrors()) > 0:
		var parts []string
		for k, v := range api.FieldErrors() {
			parts = append(parts, k+": "+v)
		}
		return strings.Join(parts, "; ")
	case errors.Is(err, bubblesclient.ErrServerIdentityChanged):
		return "The server certificate does not match the one you trusted."
	case errors.Is(err, bubblesclient.ErrUnavailable):
		return "Cannot reach the server."
	}
	return "Something went wrong. Try again."
}
