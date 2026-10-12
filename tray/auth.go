package main

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/donaldsteele/bubbles/pkg/bubblesclient"
	"github.com/donaldsteele/bubbles/tray/internal/core"
)

// ViewState is what the native sign-in window renders. The window never
// receives the device token, only text and the fingerprint to compare.
type ViewState struct {
	// Screen is "server", "confirm", "login" or "done".
	Screen    string       `json:"screen"`
	Notice    string       `json:"notice"`
	ServerURL string       `json:"serverUrl"`
	Port      string       `json:"port"`
	Prompt    *core.Prompt `json:"prompt"`
	// Reconfirm is true when an already trusted server changed its identity.
	Reconfirm  bool   `json:"reconfirm"`
	DeviceName string `json:"deviceName"`
	// CanInstall is false where the CA must be trusted by hand (Linux).
	CanInstall bool `json:"canInstall"`
}

// AuthService is the Go side of the sign-in window (bound to the frontend).
// Its methods are the only entry points the window has; none of them returns
// a secret. Every method first runs the window gate: a call that does not come
// from the sign-in window is refused and does nothing.
type AuthService struct {
	app *trayApp
	// guard is the window gate. Nil means requireSignInWindow; tests inject one.
	guard func(context.Context) error
}

func (s *AuthService) allow(ctx context.Context) error {
	if s.guard != nil {
		return s.guard(ctx)
	}
	return requireSignInWindow(ctx)
}

// State returns the screen to show.
func (s *AuthService) State(ctx context.Context) (ViewState, error) {
	if err := s.allow(ctx); err != nil {
		return ViewState{}, err
	}
	return s.app.viewState(), nil
}

// Discover browses the network for bubbles servers (mDNS).
func (s *AuthService) Discover(ctx context.Context) ([]ServerInfo, error) {
	if err := s.allow(ctx); err != nil {
		return nil, err
	}
	dctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	return discoverServers(dctx), nil
}

// Prepare downloads the CA of the server at serverURL from its onboarding
// port and returns the fingerprint to confirm. port is the onboarding port,
// empty for the default.
func (s *AuthService) Prepare(ctx context.Context, serverURL, port string) (ViewState, error) {
	if err := s.allow(ctx); err != nil {
		return ViewState{}, err
	}
	pctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.app.prepare(pctx, serverURL, port, false), nil
}

// Confirm pins the fingerprint the user compared. When trustCA is true the CA
// is also added to the user's trust store so the bubbles window can open the
// server. The fingerprint must match the one shown.
func (s *AuthService) Confirm(ctx context.Context, fingerprint string, trustCA bool) (ViewState, error) {
	if err := s.allow(ctx); err != nil {
		return ViewState{}, err
	}
	return s.app.confirm(fingerprint, trustCA), nil
}

// Login signs this device in and opens bubbles.
func (s *AuthService) Login(ctx context.Context, username, password, deviceName string) (ViewState, error) {
	if err := s.allow(ctx); err != nil {
		return ViewState{}, err
	}
	return s.app.login(username, password, deviceName), nil
}

// Back returns to the server screen.
func (s *AuthService) Back(ctx context.Context) (ViewState, error) {
	if err := s.allow(ctx); err != nil {
		return ViewState{}, err
	}
	return s.app.backToServer(), nil
}

func defaultDeviceName() string {
	h, err := os.Hostname()
	if err != nil || strings.TrimSpace(h) == "" {
		return "Desktop"
	}
	return h
}

func (a *trayApp) viewState() ViewState {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.flow == nil {
		a.flow = &flow{screen: "server", serverURL: a.cfg.ServerURL}
	}
	f := a.flow
	return ViewState{
		Screen: f.screen, Notice: f.notice, ServerURL: f.serverURL, Port: f.port, Prompt: f.prompt,
		Reconfirm: f.reconfirm, DeviceName: defaultDeviceName(), CanInstall: runtime.GOOS != "linux",
	}
}

func (a *trayApp) setFlow(f *flow) ViewState {
	a.mu.Lock()
	a.flow = f
	a.mu.Unlock()
	return a.viewState()
}

func (a *trayApp) backToServer() ViewState {
	a.mu.Lock()
	url := ""
	if a.flow != nil {
		url = a.flow.serverURL
	}
	a.mu.Unlock()
	return a.setFlow(&flow{screen: "server", serverURL: url})
}

// prepare fetches the CA and decides which screen comes next: the password
// screen when the CA is the one the user already confirmed for this server,
// the confirmation screen otherwise (first use, changed CA, reconfirm).
func (a *trayApp) prepare(ctx context.Context, rawURL, port string, reconfirm bool) ViewState {
	fail := func(msg string) ViewState {
		return a.setFlow(&flow{screen: "server", notice: msg, serverURL: rawURL, port: port})
	}
	serverURL, err := core.NormalizeServerURL(rawURL)
	if err != nil {
		return fail(err.Error())
	}
	onboarding, err := core.OnboardingURLFor(serverURL, port)
	if err != nil {
		return fail(err.Error())
	}
	der, fp, err := bubblesclient.FetchCA(ctx, onboarding)
	if err != nil {
		return fail("Could not download the server certificate. Check the address and that the onboarding port (default 8080) is reachable.")
	}
	trust := a.trustFor(serverURL)
	prompt, err := trust.Propose(fp)
	if err != nil {
		return fail("The server returned an unusable certificate.")
	}
	f := &flow{
		serverURL: serverURL, onboardingURL: onboarding, port: port, der: der, trust: trust, reconfirm: reconfirm,
	}
	if trust.Usable() && !reconfirm {
		f.screen = "login"
	} else {
		f.screen, f.prompt = "confirm", &prompt
	}
	return a.setFlow(f)
}

func (a *trayApp) confirm(fingerprint string, trustCA bool) ViewState {
	a.mu.Lock()
	f := a.flow
	a.mu.Unlock()
	if f == nil || f.screen != "confirm" || f.trust == nil {
		return a.backToServer()
	}
	pinned, err := f.trust.Confirm(fingerprint)
	if err != nil {
		a.setFlowNotice("The fingerprint you confirmed is not the one shown. Check it again.")
		return a.viewState()
	}
	note := ""
	if trustCA {
		ctx, cancel := context.WithTimeout(context.Background(), caHelperTimeout+10*time.Second)
		msg, err := installCA(ctx, a.dataDir, f.der, pinned)
		cancel()
		if err != nil {
			note = "Could not add the certificate to your trusted roots (" + err.Error() + "). The bubbles window may show a certificate warning."
		} else {
			note = msg
		}
	}
	if f.reconfirm {
		return a.finishReconfirm(f, pinned, note)
	}
	f.der = nil
	a.mu.Lock()
	f.screen, f.prompt, f.notice = "login", nil, note
	a.mu.Unlock()
	return a.viewState()
}

// finishReconfirm re-pins the CA of an already signed-in server after the
// user confirmed the new fingerprint, then reconnects with the stored token.
func (a *trayApp) finishReconfirm(f *flow, pinned, note string) ViewState {
	a.mu.Lock()
	a.cfg.CAFingerprint, a.cfg.OnboardingURL = pinned, f.onboardingURL
	a.trust = f.trust
	cfg := a.cfg
	a.mu.Unlock()
	if err := a.store.Save(cfg); err != nil {
		a.logger.Warn("save config failed", "err", err)
	}
	token, err := a.tokens.Get(cfg.ServerURL)
	if err != nil || token == "" {
		a.forgetDevice()
		a.markSignedIn(false)
		f.screen, f.prompt, f.reconfirm, f.notice = "login", nil, false, "Sign in again to continue."
		return a.viewState()
	}
	a.markSignedIn(true)
	a.startMonitor(token)
	a.mu.Lock()
	a.flow = nil
	a.mu.Unlock()
	go a.closeLoginWindow()
	return ViewState{Screen: "done", Notice: strings.TrimSpace("Reconnected. " + note)}
}

func (a *trayApp) login(username, password, deviceName string) ViewState {
	a.mu.Lock()
	f := a.flow
	a.mu.Unlock()
	if f == nil || f.screen != "login" || f.trust == nil || !f.trust.Usable() {
		return a.backToServer()
	}
	deviceName = strings.TrimSpace(deviceName)
	if username == "" || password == "" {
		a.setFlowNotice("Enter your username and password.")
		return a.viewState()
	}
	if deviceName == "" {
		deviceName = defaultDeviceName()
	}
	pin := f.trust.Pinned()
	client, err := bubblesclient.New(bubblesclient.Config{BaseURL: f.serverURL, CAFingerprint: pin, UserAgent: userAgent})
	if err != nil {
		a.setFlowNotice("The server address is not valid.")
		return a.viewState()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := client.DeviceLogin(ctx, username, password, deviceName)
	if err != nil {
		if errors.Is(err, bubblesclient.ErrServerIdentityChanged) {
			f.trust.IdentityChanged()
			return a.setFlow(&flow{
				screen: "server", serverURL: f.serverURL, port: f.port,
				notice: "The server certificate changed since you confirmed it. Start again and compare the fingerprint.",
			})
		}
		a.setFlowNotice(clientError(err))
		return a.viewState()
	}
	if err := a.tokens.Set(f.serverURL, res.DeviceToken); err != nil {
		a.logger.Warn("storing the device token failed", "err", err)
		a.revokeFresh(f.serverURL, pin, res.DeviceToken, res.DeviceID)
		a.setFlowNotice("The device token could not be stored in the system keychain.")
		return a.viewState()
	}
	cfg := core.Config{
		ServerURL: f.serverURL, OnboardingURL: f.onboardingURL, CAFingerprint: pin,
		DeviceID: res.DeviceID, Username: username, DeviceName: deviceName,
	}
	if err := a.store.Save(cfg); err != nil {
		a.logger.Warn("save config failed", "err", err)
		_ = a.tokens.Delete(f.serverURL)
		a.revokeFresh(f.serverURL, pin, res.DeviceToken, res.DeviceID)
		a.setFlowNotice("The settings could not be saved.")
		return a.viewState()
	}
	a.mu.Lock()
	a.cfg = cfg
	a.trust = f.trust
	a.flow = nil
	a.mu.Unlock()
	a.markSignedIn(true)
	a.startMonitor(res.DeviceToken)
	url := client.HandoffURL(res.HandoffCode)
	go func() {
		a.closeLoginWindow()
		a.closeMainWindow()
		a.showMainWindow(url)
	}()
	return ViewState{Screen: "done", Notice: "Signed in."}
}

// revokeFresh undoes a device login whose token could not be kept.
func (a *trayApp) revokeFresh(server, pin, token, deviceID string) {
	c, err := bubblesclient.New(bubblesclient.Config{BaseURL: server, CAFingerprint: pin, Token: token, UserAgent: userAgent})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.RevokeDevice(ctx, deviceID); err != nil {
		a.logger.Warn("revoking the unused device failed", "err", err)
	}
}
