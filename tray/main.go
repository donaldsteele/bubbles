// Command bubbles-tray is the desktop companion of a bubbles server: a tray
// icon with native notifications, a sign-in window and a window that opens
// the web app already signed in. It is a separate module (Wails v3, CGO on
// macOS and Linux) and is never imported by the server.
package main

import (
	"embed"
	"flag"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

// instanceID makes the tray a single instance per user.
const instanceID = "com.bubbles.tray"

func main() {
	background := flag.Bool("background", false, "start in the tray without opening a window")
	flag.Parse()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfgDir, err := os.UserConfigDir()
	if err != nil {
		log.Fatalf("locate the config directory: %v", err)
	}
	dataDir := filepath.Join(cfgDir, "bubbles-tray")

	n := newNotifier(logger)
	tray, err := newTrayApp(logger, dataDir, keychain{}, n)
	if err != nil {
		log.Fatal(err)
	}

	app := application.New(application.Options{
		Name:        "bubbles",
		Description: "bubbles desktop notifications",
		Services: []application.Service{
			application.NewService(n.svc),
			application.NewService(&AuthService{app: tray}),
		},
		// The origin gate runs first in the asset chain, so runtime calls from
		// remote server pages never reach the bindings or the runtime transport.
		Assets: application.AssetOptions{
			Handler:    application.AssetFileServerFS(assets),
			Middleware: originGate(runtime.GOOS),
		},
		RawMessageHandler: rawMessageGate(logger),
		Mac: application.MacOptions{
			ActivationPolicy: application.ActivationPolicyAccessory,
		},
		Windows: application.WindowsOptions{DisableQuitOnLastWindowClosed: true},
		Linux:   application.LinuxOptions{DisableQuitOnLastWindowClosed: true},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: instanceID,
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				go tray.openBubbles("")
			},
		},
	})
	tray.app = app
	tray.ui = newTrayUI(app, tray)

	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go tray.start(*background)
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
