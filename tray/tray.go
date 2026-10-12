package main

import (
	"fmt"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/donaldsteele/bubbles/tray/internal/autostart"
	"github.com/donaldsteele/bubbles/tray/internal/core"
)

// trayUI is the system tray icon and its menu.
type trayUI struct {
	tray *application.SystemTray
	menu *application.Menu

	status    *application.MenuItem
	open      *application.MenuItem
	pause1h   *application.MenuItem
	pauseTom  *application.MenuItem
	resume    *application.MenuItem
	autostart *application.MenuItem
	account   *application.MenuItem
	quit      *application.MenuItem

	app *trayApp
}

func newTrayUI(wails *application.App, a *trayApp) *trayUI {
	u := &trayUI{app: a}
	u.tray = wails.SystemTray.New()
	u.tray.SetIcon(core.IconPNG(core.IndicatorNormal))
	if runtime.GOOS == "darwin" {
		u.tray.SetTemplateIcon(core.IconPNG(core.IndicatorNormal))
	}
	u.tray.SetTooltip("bubbles")

	m := wails.NewMenu()
	u.status = m.Add("Starting...").SetEnabled(false)
	m.AddSeparator()
	u.open = m.Add("Open bubbles").OnClick(func(*application.Context) { go a.openBubbles("") })
	m.AddSeparator()
	u.pause1h = m.Add("Pause notifications for 1 hour").OnClick(func(*application.Context) { a.pause("1h") })
	u.pauseTom = m.Add("Pause notifications until tomorrow").OnClick(func(*application.Context) { a.pause("tomorrow") })
	u.resume = m.Add("Resume notifications").SetHidden(true).OnClick(func(*application.Context) { a.resume() })
	m.AddSeparator()
	on, _ := autostart.Enabled()
	u.autostart = m.AddCheckbox("Start at login", on).OnClick(func(*application.Context) { go a.toggleAutostart() })
	m.AddSeparator()
	u.account = m.Add("Sign in...").OnClick(func(*application.Context) { go u.accountClicked() })
	u.quit = m.Add("Quit").OnClick(func(*application.Context) { wails.Quit() })
	u.menu = m
	u.tray.SetMenu(m)
	if runtime.GOOS != "darwin" {
		u.tray.OnClick(func() { go a.openBubbles("") })
		u.tray.OnRightClick(func() { u.tray.OpenMenu() })
	}
	return u
}

func (u *trayUI) accountClicked() {
	u.app.mu.Lock()
	in := u.app.signedIn
	u.app.mu.Unlock()
	if in {
		u.app.signOut()
		return
	}
	u.app.beginSignIn("")
}

func (u *trayUI) setIndicator(ind core.Indicator) {
	if u == nil {
		return
	}
	icon := core.IconPNG(ind)
	u.tray.SetIcon(icon)
	if runtime.GOOS == "darwin" && ind == core.IndicatorNormal {
		u.tray.SetTemplateIcon(icon)
	}
	tip := "bubbles"
	switch ind {
	case core.IndicatorUnread:
		tip = "bubbles: unread messages"
	case core.IndicatorCritical:
		tip = "bubbles: critical alert"
	}
	u.tray.SetTooltip(tip)
}

func (u *trayUI) setStatus(text string) {
	if u == nil {
		return
	}
	u.status.SetLabel(text)
	u.menu.Update()
}

func (u *trayUI) setSignedIn(in bool) {
	if u == nil {
		return
	}
	u.open.SetEnabled(in)
	u.pause1h.SetEnabled(in)
	u.pauseTom.SetEnabled(in)
	if in {
		u.account.SetLabel("Sign out")
	} else {
		u.account.SetLabel("Sign in...")
	}
	u.menu.Update()
}

// refreshPause shows how long notifications are paused, or hides the resume
// item once the pause is over.
func (u *trayUI) refreshPause() {
	if u == nil {
		return
	}
	until := u.app.pausedUntil()
	if until.IsZero() {
		u.resume.SetHidden(true)
		u.pause1h.SetHidden(false)
		u.pauseTom.SetHidden(false)
	} else {
		u.resume.SetLabel(fmt.Sprintf("Resume notifications (paused until %s)", until.Local().Format("15:04")))
		u.resume.SetHidden(false)
		u.pause1h.SetHidden(true)
		u.pauseTom.SetHidden(true)
	}
	u.menu.Update()
}

func (u *trayUI) refreshAutostart() {
	if u == nil {
		return
	}
	on, _ := autostart.Enabled()
	u.autostart.SetChecked(on)
	u.menu.Update()
}
