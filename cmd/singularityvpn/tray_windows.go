//go:build windows

package main

import (
	"runtime"
	"sync"

	"fyne.io/systray"

	"singularity-vpn-app/internal/ipc"
)

// tray is the notification-area icon. It runs on its own OS thread with
// its own message loop: on Windows the tray's hidden window receives its
// messages on the thread that created it, and systray.Run creates and pumps
// on the same locked thread, independent of the thread Wails runs on.
type tray struct {
	app *App

	ready  chan struct{}
	toggle *systray.MenuItem

	mu        sync.Mutex
	connected bool // what the toggle item currently offers to undo
}

func start_tray(app *App) *tray {
	t := &tray{app: app, ready: make(chan struct{})}
	go func() {
		runtime.LockOSThread()
		systray.Run(t.on_ready, nil)
	}()
	return t
}

func (t *tray) on_ready() {
	systray.SetIcon(icon_ico(false))
	systray.SetTooltip("Singularity VPN")

	open := systray.AddMenuItem("Открыть", "Показать окно")
	t.toggle = systray.AddMenuItem("Подключить", "")
	systray.AddSeparator()
	quit := systray.AddMenuItem("Выход", "Закрыть окно и иконку; туннелем управляет служба, он останется как есть")
	close(t.ready)

	go func() {
		for {
			select {
			case <-open.ClickedCh:
				t.app.show()
			case <-t.toggle.ClickedCh:
				t.mu.Lock()
				connected := t.connected
				t.mu.Unlock()
				go func() {
					if connected {
						t.app.Disconnect()
					} else {
						t.app.Connect()
					}
				}()
			case <-quit.ClickedCh:
				systray.Quit()
				t.app.quit()
				return
			}
		}
	}()
}

// show_status brings the icon, tooltip and toggle item in line with status;
// nil means the service is not running.
func (t *tray) show_status(status *ipc.Status_result) {
	if t == nil {
		return
	}
	select {
	case <-t.ready:
	default:
		return // the tray is not up yet; the next status will reach it
	}

	tooltip := "Singularity VPN — служба не запущена"
	connected, offer := false, "Подключить"
	if status != nil {
		switch status.State {
		case "connected", "degraded":
			connected, offer = true, "Отключить"
			tooltip = "Singularity VPN — подключено"
			if place := place_of(status); place != "" {
				tooltip += ": " + place
			}
			if name := protocol_names[status.Protocol]; name != "" {
				tooltip += " (" + name + ")"
			}
		case "connecting", "switching":
			connected, offer = true, "Отключить"
			tooltip = "Singularity VPN — подключение…"
		case "disconnecting":
			tooltip = "Singularity VPN — отключение…"
		default:
			tooltip = "Singularity VPN — не подключено"
		}
		if !status.Signed_in {
			tooltip = "Singularity VPN — нужно войти"
		}
	}

	t.mu.Lock()
	t.connected = connected
	t.mu.Unlock()

	systray.SetIcon(icon_ico(connected))
	systray.SetTooltip(tooltip)
	t.toggle.SetTitle(offer)
	if status == nil || !status.Signed_in {
		t.toggle.Disable()
	} else {
		t.toggle.Enable()
	}
}

// protocol_names spells the protocols the way their authors do, as the
// window does.
var protocol_names = map[string]string{"wireguard": "WireGuard", "amneziawg": "AmneziaWG", "openvpn": "OpenVPN"}

// place_of names the node as the control plane described it, or nothing.
func place_of(status *ipc.Status_result) string {
	switch {
	case status.Country != "" && status.City != "":
		return status.Country + " · " + status.City
	case status.Country != "":
		return status.Country
	default:
		return status.Node_name
	}
}
