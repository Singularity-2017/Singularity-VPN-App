package main

import "testing"

func Test_the_portal_link_follows_the_current_control_plane(t *testing.T) {
	cases := map[string]string{
		"http://203.0.113.7/vpn":    "http://203.0.113.7/vpn/client/",
		"https://192.0.2.4/vpn/":    "https://192.0.2.4/vpn/client/",
		"https://192.0.2.4/vpn?x=1": "https://192.0.2.4/vpn/client/",
	}
	for base, want := range cases {
		if got, ok := portal_link(base); !ok || got != want {
			t.Errorf("portal_link(%q) = %q, %v; want %q", base, got, ok, want)
		}
	}
	for _, bad := range []string{"", "ftp://192.0.2.4", "http://user@192.0.2.4", "/vpn"} {
		if got, ok := portal_link(bad); ok {
			t.Errorf("portal_link(%q) = %q, want refused", bad, got)
		}
	}
}
