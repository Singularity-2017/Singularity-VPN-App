package main

import (
	"net/url"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"singularity-vpn-app/internal/buildinfo"
)

// Open_account_portal opens the configured service's portal without credentials.
// A page cannot supply a destination or put the service token in the URL.
func (a *App) Open_account_portal() Outcome {
	destination, err := url.Parse(buildinfo.Control_plane)
	if err != nil || destination.Host == "" || destination.User != nil || (destination.Scheme != "https" && destination.Scheme != "http") {
		return Outcome{Code: code_failed, Message: "account portal is not configured"}
	}
	destination.Path = strings.TrimRight(destination.Path, "/") + "/client/"
	destination.RawPath, destination.RawQuery, destination.Fragment = "", "", ""
	runtime.BrowserOpenURL(a.ctx, destination.String())
	return Outcome{}
}
