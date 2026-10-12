package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/donaldsteele/bubbles/pkg/bubblesclient"
	"github.com/donaldsteele/bubbles/pkg/protocol"
	"github.com/donaldsteele/bubbles/tray/internal/core"
)

// ViewState is what the native sign-in window renders. The window never
// receives the device token, only text and the fingerprint to compare.
type ViewState struct {
	// Screen is "server", "confirm", "account" or "done".
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
	// Instance is the server's instance name, empty when unknown.
	Instance string `json:"instance"`
	// Address is the host:port of the server, for display.
	Address string `json:"address"`
	// RegistrationOpen is true when sign-up is open or could not be checked.
	RegistrationOpen bool `json:"registrationOpen"`
	// DefaultTab is "create" or "signin": the tab the account screen opens on.
	DefaultTab string `json:"defaultTab"`
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

// PrepareNamed is Prepare for a server picked from the discovery list; name is
// its instance name.
func (s *AuthService) PrepareNamed(ctx context.Context, serverURL, port, name string) (ViewState, error) {
	if err := s.allow(ctx); err != nil {
		return ViewState{}, err
	}
	pctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.app.prepareNamed(pctx, serverURL, port, name, false), nil
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

// Register creates an account on the server and signs this device in.
func (s *AuthService) Register(ctx context.Context, name, password, deviceName string) (ViewState, error) {
	if err := s.allow(ctx); err != nil {
		return ViewState{}, err
	}
	return s.app.register(name, password, deviceName), nil
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
		Instance: f.instance, Address: core.ServerAddress(f.serverURL),
		RegistrationOpen: f.registrationOpen, DefaultTab: f.defaultTab,
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
	return a.prepareNamed(ctx, rawURL, port, "", reconfirm)
}

func (a *trayApp) prepareNamed(ctx context.Context, rawURL, port, name string, reconfirm bool) ViewState {
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
		return fail("Could not download the server certificate. Check the address and that the onboarding port (default 28226) is reachable.")
	}
	trust := a.trustFor(serverURL)
	prompt, err := trust.Propose(fp)
	if err != nil {
		return fail("The server returned an unusable certificate.")
	}
	f := &flow{
		serverURL: serverURL, onboardingURL: onboarding, port: port, der: der, trust: trust, reconfirm: reconfirm,
		instance: strings.TrimSpace(name),
	}
	if trust.Usable() && !reconfirm {
		return a.openAccount(f, "")
	}
	f.screen, f.prompt = "confirm", &prompt
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
		if errors.Is(err, core.ErrUnconstrainedCA) {
			a.setFlowNotice("This server's certificate is not limited to your local network, so bubbles did not install it. Ask the admin.")
			return a.viewState()
		}
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
	return a.openAccount(f, note)
}

// statusTimeout bounds the sign-up check on the account screen.
const statusTimeout = 5 * time.Second

// openAccount moves f to the account screen. It asks the server, over the
// pinned connection, whether sign-up is open; when that fails it shows both
// tabs and lets the server answer.
func (a *trayApp) openAccount(f *flow, note string) ViewState {
	open, known := true, false
	if f.trust != nil && f.trust.Usable() {
		cfg := bubblesclient.Config{BaseURL: f.serverURL, CAFingerprint: f.trust.Pinned(), UserAgent: userAgent}
		if c, err := bubblesclient.New(cfg); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
			st, err := c.Status(ctx)
			cancel()
			if err == nil && st != nil {
				open, known = st.RegistrationOpen, true
				if f.instance == "" {
					f.instance = strings.TrimSpace(st.InstanceName)
				}
			}
		}
	}
	a.mu.Lock()
	saved := ""
	if a.cfg.ServerURL == f.serverURL {
		saved = a.cfg.Username
	}
	f.screen, f.prompt, f.notice = "account", nil, note
	f.registrationOpen = open
	f.defaultTab = core.DefaultTab(open && known, saved)
	a.flow = f
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
		f.reconfirm = false
		return a.openAccount(f, "Sign in again to continue.")
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
	f, deviceName, ok := a.accountFlow(deviceName)
	if !ok {
		return a.backToServer()
	}
	if username == "" || password == "" {
		a.setFlowNotice("Enter your name and password.")
		return a.viewState()
	}
	return a.authenticate(f, username, deviceName, func(ctx context.Context, c *bubblesclient.Client) (*protocol.DeviceLoginResponse, error) {
		return c.DeviceLogin(ctx, username, password, deviceName)
	}, clientError)
}

func (a *trayApp) register(name, password, deviceName string) ViewState {
	f, deviceName, ok := a.accountFlow(deviceName)
	if !ok {
		return a.backToServer()
	}
	name = strings.TrimSpace(name)
	if name == "" || password == "" {
		a.setFlowNotice("Enter your name and a password.")
		return a.viewState()
	}
	return a.authenticate(f, name, deviceName, func(ctx context.Context, c *bubblesclient.Client) (*protocol.DeviceLoginResponse, error) {
		return c.DeviceRegister(ctx, name, password, deviceName)
	}, registerError)
}

// accountFlow returns the current flow when it is on the account screen with
// a usable trust, and the device name to use.
func (a *trayApp) accountFlow(deviceName string) (*flow, string, bool) {
	a.mu.Lock()
	f := a.flow
	a.mu.Unlock()
	if f == nil || f.screen != "account" || f.trust == nil || !f.trust.Usable() {
		return nil, "", false
	}
	deviceName = strings.TrimSpace(deviceName)
	if deviceName == "" {
		deviceName = defaultDeviceName()
	}
	return f, deviceName, true
}

// authenticate runs a device login or registration and, on success, stores
// the token and config, starts the monitor and opens the chat window.
func (a *trayApp) authenticate(
	f *flow, username, deviceName string,
	do func(context.Context, *bubblesclient.Client) (*protocol.DeviceLoginResponse, error),
	describe func(error) string,
) ViewState {
	pin := f.trust.Pinned()
	client, err := bubblesclient.New(bubblesclient.Config{BaseURL: f.serverURL, CAFingerprint: pin, UserAgent: userAgent})
	if err != nil {
		a.setFlowNotice("The server address is not valid.")
		return a.viewState()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := do(ctx, client)
	if err != nil {
		if errors.Is(err, bubblesclient.ErrServerIdentityChanged) {
			f.trust.IdentityChanged()
			return a.setFlow(&flow{
				screen: "server", serverURL: f.serverURL, port: f.port,
				notice: "The server certificate changed since you confirmed it. Start again and compare the fingerprint.",
			})
		}
		a.setFlowNotice(describe(err))
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

// registerError describes a failed sign-up.
func registerError(err error) string {
	var api *bubblesclient.APIError
	if errors.As(err, &api) {
		fields := api.FieldErrors()
		switch {
		case api.Status == http.StatusForbidden:
			return "Sign-up is closed. Ask the admin for an invite link."
		case api.Status == http.StatusConflict:
			return "That name is already taken. Pick another, or sign in if it is you."
		case api.Status == http.StatusBadRequest && len(fields) > 0:
			return fieldErrorText(fields)
		}
	}
	return clientError(err)
}

// fieldErrorText turns per-field server errors into readable text.
func fieldErrorText(fields map[string]string) string {
	labels := map[string]string{"name": "Your name", "password": "Password", "device_name": "Name of this computer"}
	var parts []string
	for _, k := range []string{"name", "password", "device_name"} {
		if v, ok := fields[k]; ok {
			parts = append(parts, labels[k]+": "+v)
		}
	}
	for k, v := range fields {
		if _, known := labels[k]; !known {
			parts = append(parts, k+": "+v)
		}
	}
	return strings.Join(parts, ". ")
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
