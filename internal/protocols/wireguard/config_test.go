package wireguard

import (
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Keys used across this file. Each is a real, randomly generated 32-byte
// Curve25519-shaped key, valid base64, so Parse never trips on encoding
// while a test is exercising something else.
const (
	private_key_full   = "MVge1nFDeRVlPsb9GmtIXuLlooewnRqaq9CXtIXO3lE="
	public_key_full    = "jpc3v7+vBYSdHyvP61zD2ZC/qsbUyw30aKW6TAN6C0g="
	preshared_key_full = "P4nOeVWePHOiNB3DTUOqs6LUGxO+LXzsZqSMvde8uYA="

	private_key_split   = "IXPG/VviFLhDFUY5qo1n3DeXd/xFgDrKQEjlKmKGFxs="
	public_key_split    = "cTMflzjV4ZL980O38YO7aGNkc4T2fkwu7CFntJDmj/E="
	preshared_key_split = "EC7SPnUV3BSztaju2cFjzQSZ6HDqgWPVOlsTvpnnokE="

	public_key_second    = "nN5D5dR+bVi9rt4SLtuVPmIR/us4tp9ij8ztcVSI7+M="
	preshared_key_second = "SwdLXevRXUwYLu+8D+rhqJ3/dx2O9VwZ7TSJ8sb21Jk="
)

// full_tunnel_config is the real full-tunnel config from the specification,
// with the placeholder keys replaced by real ones.
func full_tunnel_config() string {
	return "[Interface]\n" +
		"PrivateKey = " + private_key_full + "\n" +
		"Address = 10.0.0.2/32\n" +
		"DNS = 1.1.1.1, 1.0.0.1\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = " + public_key_full + "\n" +
		"PresharedKey = " + preshared_key_full + "\n" +
		"Endpoint = vpn.example.org:51080\n" +
		"AllowedIPs = 0.0.0.0/0\n" +
		"PersistentKeepalive = 15\n"
}

// split_tunnel_config is the real split-tunnel config from the
// specification, with the placeholder keys replaced by real ones.
func split_tunnel_config() string {
	return "[Interface]\n" +
		"PrivateKey = " + private_key_split + "\n" +
		"Address = 10.29.8.5/32\n" +
		"DNS = 10.29.8.1\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = " + public_key_split + "\n" +
		"PresharedKey = " + preshared_key_split + "\n" +
		"Endpoint = vpn.example.org:51443\n" +
		"AllowedIPs = 10.29.8.0/24, 5.255.255.0/24, 77.88.8.0/24\n" +
		"PersistentKeepalive = 15\n"
}

// without_line drops every line whose trimmed text starts with prefix, so a
// test can knock out exactly one field from a real config.
func without_line(config, prefix string) string {
	var kept []string
	for _, line := range strings.Split(config, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func must_addr(t *testing.T, s string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("netip.ParseAddr(%q): %v", s, err)
	}
	return addr
}

func must_prefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	prefix, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatalf("netip.ParsePrefix(%q): %v", s, err)
	}
	return prefix
}

func Test_the_real_full_tunnel_config_parses(t *testing.T) {
	config, err := Parse(full_tunnel_config())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want_private, _ := Parse_key(private_key_full)
	if config.Private_key != want_private {
		t.Fatalf("Private_key = %v, want %v", config.Private_key, want_private)
	}
	if len(config.Addresses) != 1 || config.Addresses[0] != must_prefix(t, "10.0.0.2/32") {
		t.Fatalf("Addresses = %v", config.Addresses)
	}
	want_dns := []netip.Addr{must_addr(t, "1.1.1.1"), must_addr(t, "1.0.0.1")}
	if len(config.Dns) != 2 || config.Dns[0] != want_dns[0] || config.Dns[1] != want_dns[1] {
		t.Fatalf("Dns = %v, want %v", config.Dns, want_dns)
	}
	if config.Mtu != 1420 {
		t.Fatalf("Mtu = %d, want 1420 (default)", config.Mtu)
	}
	if len(config.Peers) != 1 {
		t.Fatalf("Peers = %v, want exactly one", config.Peers)
	}

	peer := config.Peers[0]
	want_public, _ := Parse_key(public_key_full)
	want_preshared, _ := Parse_key(preshared_key_full)
	if peer.Public_key != want_public {
		t.Fatalf("Public_key = %v, want %v", peer.Public_key, want_public)
	}
	if !peer.Has_preshared || peer.Preshared_key != want_preshared {
		t.Fatalf("Preshared_key = %v, has_preshared = %v", peer.Preshared_key, peer.Has_preshared)
	}
	if peer.Endpoint != "vpn.example.org:51080" {
		t.Fatalf("Endpoint = %q", peer.Endpoint)
	}
	if len(peer.Allowed_ips) != 1 || peer.Allowed_ips[0] != must_prefix(t, "0.0.0.0/0") {
		t.Fatalf("Allowed_ips = %v", peer.Allowed_ips)
	}
	if peer.Keepalive != 15*time.Second {
		t.Fatalf("Keepalive = %v, want 15s", peer.Keepalive)
	}
}

func Test_the_real_split_tunnel_config_parses(t *testing.T) {
	config, err := Parse(split_tunnel_config())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(config.Addresses) != 1 || config.Addresses[0] != must_prefix(t, "10.29.8.5/32") {
		t.Fatalf("Addresses = %v", config.Addresses)
	}
	if len(config.Dns) != 1 || config.Dns[0] != must_addr(t, "10.29.8.1") {
		t.Fatalf("Dns = %v", config.Dns)
	}
	if len(config.Peers) != 1 {
		t.Fatalf("Peers = %v, want exactly one", config.Peers)
	}

	peer := config.Peers[0]
	want := []netip.Prefix{
		must_prefix(t, "10.29.8.0/24"),
		must_prefix(t, "5.255.255.0/24"),
		must_prefix(t, "77.88.8.0/24"),
	}
	if len(peer.Allowed_ips) != len(want) {
		t.Fatalf("Allowed_ips = %v, want %v", peer.Allowed_ips, want)
	}
	for i := range want {
		if peer.Allowed_ips[i] != want[i] {
			t.Fatalf("Allowed_ips[%d] = %v, want %v", i, peer.Allowed_ips[i], want[i])
		}
	}
	if peer.Endpoint != "vpn.example.org:51443" {
		t.Fatalf("Endpoint = %q", peer.Endpoint)
	}
}

// Every required field, missing one at a time, the way
// internal/api/incomplete_response_test.go does it.
func Test_a_config_missing_a_field_this_client_acts_on_is_refused(t *testing.T) {
	cases := []struct {
		name   string
		config string
		want   string
	}{
		{"PrivateKey", without_line(full_tunnel_config(), "PrivateKey"), "PrivateKey"},
		{"Address", without_line(full_tunnel_config(), "Address"), "Address"},
		{"PublicKey", without_line(full_tunnel_config(), "PublicKey"), "PublicKey"},
		{"Endpoint", without_line(full_tunnel_config(), "Endpoint"), "Endpoint"},
		{"AllowedIPs", without_line(full_tunnel_config(), "AllowedIPs"), "AllowedIPs"},
	}

	for _, c := range cases {
		_, err := Parse(c.config)
		if !errors.Is(err, Err_incomplete_config) {
			t.Fatalf("%s: err = %v, want Err_incomplete_config", c.name, err)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: err = %q, which does not name the field", c.name, err)
		}
	}
}

// Every missing field at once, in one message, rather than one per round of
// discovery.
func Test_a_config_missing_every_field_at_once_names_them_all(t *testing.T) {
	config := full_tunnel_config()
	for _, prefix := range []string{"PrivateKey", "Address", "PublicKey", "Endpoint", "AllowedIPs"} {
		config = without_line(config, prefix)
	}

	_, err := Parse(config)
	if !errors.Is(err, Err_incomplete_config) {
		t.Fatalf("err = %v, want Err_incomplete_config", err)
	}
	for _, field := range []string{"PrivateKey", "Address", "PublicKey", "Endpoint", "AllowedIPs"} {
		if !strings.Contains(err.Error(), field) {
			t.Fatalf("err = %q, which does not name %s", err, field)
		}
	}
}

func Test_a_config_with_no_peer_is_refused(t *testing.T) {
	config := "[Interface]\n" +
		"PrivateKey = " + private_key_full + "\n" +
		"Address = 10.0.0.2/32\n"

	_, err := Parse(config)
	if !errors.Is(err, Err_incomplete_config) {
		t.Fatalf("err = %v, want Err_incomplete_config", err)
	}
}

func Test_shell_directives_are_refused_by_name(t *testing.T) {
	cases := []struct {
		directive string
		line      string
	}{
		{"PostUp", "PostUp = curl http://evil.example/"},
		{"PreDown", "PreDown = rm -rf /"},
		{"Table", "Table = off"},
		{"SaveConfig", "SaveConfig = true"},
	}

	for _, c := range cases {
		config := "[Interface]\n" +
			"PrivateKey = " + private_key_full + "\n" +
			"Address = 10.0.0.2/32\n" +
			c.line + "\n"

		_, err := Parse(config)
		if !errors.Is(err, Err_unsupported_directive) {
			t.Fatalf("%s: err = %v, want Err_unsupported_directive", c.directive, err)
		}
		if !strings.Contains(err.Error(), c.directive) {
			t.Fatalf("%s: err = %q, which does not name the directive", c.directive, err)
		}
	}
}

func Test_an_unrecognised_key_is_refused_and_named(t *testing.T) {
	config := "[Interface]\n" +
		"PrivateKey = " + private_key_full + "\n" +
		"Address = 10.0.0.2/32\n" +
		"Frobnicate = yes\n"

	_, err := Parse(config)
	if !errors.Is(err, Err_unsupported_directive) {
		t.Fatalf("err = %v, want Err_unsupported_directive", err)
	}
	if !strings.Contains(err.Error(), "Frobnicate") {
		t.Fatalf("err = %q, which does not name the key", err)
	}
}

// An AmneziaWG config must be refused with Err_obfuscation_unsupported and
// specifically NOT with Err_unsupported_directive: the two sentinels carry
// different consequences and a caller must be able to tell them apart.
func Test_an_amnezia_config_is_refused_with_the_obfuscation_sentinel(t *testing.T) {
	config := "[Interface]\n" +
		"PrivateKey = " + private_key_full + "\n" +
		"Address = 10.0.0.2/32\n" +
		"DNS = 1.1.1.1, 1.0.0.1\n" +
		"Jc = 100\n" +
		"S1 = 0\n" +
		"H1 = 1\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = " + public_key_full + "\n" +
		"PresharedKey = " + preshared_key_full + "\n" +
		"Endpoint = vpn.example.org:51080\n" +
		"AllowedIPs = 0.0.0.0/0\n" +
		"PersistentKeepalive = 15\n"

	_, err := Parse(config)
	if !errors.Is(err, Err_obfuscation_unsupported) {
		t.Fatalf("err = %v, want Err_obfuscation_unsupported", err)
	}
	if errors.Is(err, Err_unsupported_directive) {
		t.Fatalf("err = %v, must NOT also be Err_unsupported_directive", err)
	}
}

func Test_a_dns_domain_name_is_refused_and_named(t *testing.T) {
	config := "[Interface]\n" +
		"PrivateKey = " + private_key_full + "\n" +
		"Address = 10.0.0.2/32\n" +
		"DNS = resolver.example.org\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = " + public_key_full + "\n" +
		"Endpoint = vpn.example.org:51080\n" +
		"AllowedIPs = 0.0.0.0/0\n"

	_, err := Parse(config)
	if err == nil {
		t.Fatal("expected an error for a DNS search domain")
	}
	if !strings.Contains(err.Error(), "resolver.example.org") {
		t.Fatalf("err = %q, which does not name the value", err)
	}
}

func Test_two_peers_parse_in_order(t *testing.T) {
	config := "[Interface]\n" +
		"PrivateKey = " + private_key_full + "\n" +
		"Address = 10.0.0.2/32\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = " + public_key_full + "\n" +
		"Endpoint = one.example.org:51080\n" +
		"AllowedIPs = 10.0.0.0/8\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = " + public_key_second + "\n" +
		"Endpoint = two.example.org:51081\n" +
		"AllowedIPs = 172.16.0.0/12\n"

	parsed, err := Parse(config)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(parsed.Peers) != 2 {
		t.Fatalf("Peers = %v, want 2", parsed.Peers)
	}
	want_first, _ := Parse_key(public_key_full)
	want_second, _ := Parse_key(public_key_second)
	if parsed.Peers[0].Public_key != want_first {
		t.Fatalf("Peers[0].Public_key = %v, want %v", parsed.Peers[0].Public_key, want_first)
	}
	if parsed.Peers[1].Public_key != want_second {
		t.Fatalf("Peers[1].Public_key = %v, want %v", parsed.Peers[1].Public_key, want_second)
	}
	if parsed.Peers[0].Endpoint != "one.example.org:51080" || parsed.Peers[1].Endpoint != "two.example.org:51081" {
		t.Fatalf("endpoints out of order: %v", parsed.Peers)
	}
}

func Test_mtu_defaults_and_overrides(t *testing.T) {
	if config, err := Parse(full_tunnel_config()); err != nil || config.Mtu != 1420 {
		t.Fatalf("Mtu = %d, err = %v, want 1420 with no error", config.Mtu, err)
	}

	with_mtu := "[Interface]\n" +
		"PrivateKey = " + private_key_full + "\n" +
		"Address = 10.0.0.2/32\n" +
		"MTU = 1280\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = " + public_key_full + "\n" +
		"Endpoint = vpn.example.org:51080\n" +
		"AllowedIPs = 0.0.0.0/0\n"

	config, err := Parse(with_mtu)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if config.Mtu != 1280 {
		t.Fatalf("Mtu = %d, want 1280", config.Mtu)
	}
}

func Test_persistent_keepalive_off_zero_and_a_number(t *testing.T) {
	cases := []struct {
		value string
		want  time.Duration
	}{
		{"off", 0},
		{"0", 0},
		{"15", 15 * time.Second},
	}

	for _, c := range cases {
		config := "[Interface]\n" +
			"PrivateKey = " + private_key_full + "\n" +
			"Address = 10.0.0.2/32\n" +
			"\n" +
			"[Peer]\n" +
			"PublicKey = " + public_key_full + "\n" +
			"Endpoint = vpn.example.org:51080\n" +
			"AllowedIPs = 0.0.0.0/0\n" +
			"PersistentKeepalive = " + c.value + "\n"

		parsed, err := Parse(config)
		if err != nil {
			t.Fatalf("PersistentKeepalive = %s: Parse: %v", c.value, err)
		}
		if parsed.Peers[0].Keepalive != c.want {
			t.Fatalf("PersistentKeepalive = %s: Keepalive = %v, want %v", c.value, parsed.Peers[0].Keepalive, c.want)
		}
	}
}

// wg-quick accumulates Address, DNS and AllowedIPs across repeated lines
// rather than letting the last one win, and a config written for a
// dual-stack tunnel uses exactly that form. Taking only the last line
// dropped the other family's address and half the routed networks without
// saying anything -- a tunnel that comes up looking healthy and carries
// less than it was told to.
func Test_repeated_list_valued_keys_accumulate_rather_than_replace(t *testing.T) {
	text := "[Interface]\n" +
		"PrivateKey = " + private_key_full + "\n" +
		"Address = 10.0.0.2/32\n" +
		"Address = fd00::2/128\n" +
		"DNS = 1.1.1.1\n" +
		"DNS = 1.0.0.1\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = " + public_key_full + "\n" +
		"Endpoint = vpn.example.org:51080\n" +
		"AllowedIPs = 10.29.8.0/24\n" +
		"AllowedIPs = 192.168.0.0/16, 172.16.0.0/12\n"

	config, err := Parse(text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want_addresses := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.2/32"),
		netip.MustParsePrefix("fd00::2/128"),
	}
	if !reflect.DeepEqual(config.Addresses, want_addresses) {
		t.Errorf("addresses = %v, want %v", config.Addresses, want_addresses)
	}

	want_dns := []netip.Addr{
		netip.MustParseAddr("1.1.1.1"),
		netip.MustParseAddr("1.0.0.1"),
	}
	if !reflect.DeepEqual(config.Dns, want_dns) {
		t.Errorf("dns = %v, want %v", config.Dns, want_dns)
	}

	want_allowed := []netip.Prefix{
		netip.MustParsePrefix("10.29.8.0/24"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("172.16.0.0/12"),
	}
	if !reflect.DeepEqual(config.Peers[0].Allowed_ips, want_allowed) {
		t.Errorf("allowed_ips = %v, want %v", config.Peers[0].Allowed_ips, want_allowed)
	}
}

// A key that names one value is a different matter: two PrivateKey lines is
// not a config anybody meant to write, and picking one of them is guessing
// about the thing the tunnel's identity rests on.
func Test_a_repeated_single_valued_key_is_refused_and_named(t *testing.T) {
	cases := map[string][]string{
		"PrivateKey":          {"[Interface]", "PrivateKey = " + private_key_full, "PrivateKey = " + private_key_split, "Address = 10.0.0.2/32"},
		"MTU":                 {"[Interface]", "PrivateKey = " + private_key_full, "Address = 10.0.0.2/32", "MTU = 1420", "MTU = 1280"},
		"PublicKey":           {"[Peer]", "PublicKey = " + public_key_full, "PublicKey = " + public_key_second, "Endpoint = a.example.org:1", "AllowedIPs = 0.0.0.0/0"},
		"Endpoint":            {"[Peer]", "PublicKey = " + public_key_full, "Endpoint = a.example.org:1", "Endpoint = b.example.org:2", "AllowedIPs = 0.0.0.0/0"},
		"PresharedKey":        {"[Peer]", "PublicKey = " + public_key_full, "PresharedKey = " + preshared_key_full, "PresharedKey = " + preshared_key_second, "Endpoint = a.example.org:1", "AllowedIPs = 0.0.0.0/0"},
		"PersistentKeepalive": {"[Peer]", "PublicKey = " + public_key_full, "Endpoint = a.example.org:1", "AllowedIPs = 0.0.0.0/0", "PersistentKeepalive = 15", "PersistentKeepalive = 25"},
	}

	for name, lines := range cases {
		t.Run(name, func(t *testing.T) {
			text := strings.Join(lines, "\n") + "\n"
			if lines[0] == "[Peer]" {
				text = "[Interface]\nPrivateKey = " + private_key_full + "\nAddress = 10.0.0.2/32\n" + text
			} else {
				text += "\n[Peer]\nPublicKey = " + public_key_full + "\nEndpoint = a.example.org:1\nAllowedIPs = 0.0.0.0/0\n"
			}

			_, err := Parse(text)
			if err == nil {
				t.Fatalf("Parse accepted a config with two %s lines", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("err = %v, want it to name %s", err, name)
			}
		})
	}
}
