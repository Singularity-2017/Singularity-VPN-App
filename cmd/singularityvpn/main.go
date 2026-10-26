//go:build windows

// Command singularityvpn is SingularityVPN.exe: the window and the tray
// icon, and nothing else (design section 2). It runs as the logged-in user,
// holds no credential and talks to nobody but the service, over the pipe in
// internal/ipc. Closing the window hides it; the tunnel belongs to the
// service and is not affected by this process coming or going.
package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend
var frontend embed.FS

func main() {
	assets, err := fs.Sub(frontend, "frontend")
	if err != nil {
		fmt.Fprintf(os.Stderr, "singularityvpn: the embedded interface is missing: %v\n", err)
		os.Exit(1)
	}

	app := new_app()
	err = wails.Run(&options.App{
		Title:     "Singularity VPN",
		Width:     440,
		Height:    660,
		MinWidth:  400,
		MinHeight: 480,
		// The window is a view onto the service. Closing it is "put it
		// away", the way a tray application behaves; the tray's own menu
		// is how the process ends.
		HideWindowOnClose: true,
		// --tray is how autostart launches it (the Run key svpn-setup
		// writes): a person logging in did not ask to see a window.
		StartHidden:      has_flag("--tray"),
		BackgroundColour: &options.RGBA{R: 0x20, G: 0x26, B: 0x2b, A: 0xff},
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        app.startup,
		OnShutdown:       func(ctx context.Context) { app.shutdown() },
		Bind:             []interface{}{app},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "c8f6f2a4-6a43-4b1e-9d55-singularityvpn",
			// A second launch -- the Start menu, a double click -- means
			// "show me the window", not a second copy fighting over the
			// tray.
			OnSecondInstanceLaunch: func(options.SecondInstanceData) { app.show() },
		},
		Windows: &windows.Options{
			Theme: windows.Dark,
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "singularityvpn: %v\n", err)
		os.Exit(1)
	}
}

func has_flag(flag string) bool {
	for _, a := range os.Args[1:] {
		if a == flag {
			return true
		}
	}
	return false
}
