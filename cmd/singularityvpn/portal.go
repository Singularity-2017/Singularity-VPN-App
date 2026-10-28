package main

import (
	"net/url"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Open_account_portal opens the configured service's portal without credentials.
// A page cannot supply a destination or put the service token in the URL.
// The address is the service's current one: a signed manifest may have
// moved the control plane since this build.
func (a *App) Open_account_portal() Outcome {
	link, ok := portal_link(a.Version().Control_plane)
	if !ok {
		return Outcome{Code: code_failed, Message: "account portal is not configured"}
	}
	runtime.BrowserOpenURL(a.ctx, link)
	return Outcome{}
}

// portal_link is the portal under base, with no query, fragment or user.
func portal_link(base string) (string, bool) {
	destination, err := url.Parse(base)
	if err != nil || destination.Host == "" || destination.User != nil || (destination.Scheme != "https" && destination.Scheme != "http") {
		return "", false
	}
	destination.Path = strings.TrimRight(destination.Path, "/") + "/client/"
	destination.RawPath, destination.RawQuery, destination.Fragment = "", "", ""
	return destination.String(), true
}
