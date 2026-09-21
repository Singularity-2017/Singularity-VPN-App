package wireguard

import (
	"net/netip"
	"strings"
	"testing"
)

// Uapi_set on the full-tunnel config renders exactly this string. Not a
// substring check: the order of lines is part of the contract with
// wireguard-go's UAPI parser, and a substring check would not notice
// public_key drifting out of first place in a peer block.
func Test_uapi_set_renders_the_full_tunnel_config_exactly(t *testing.T) {
	config, err := Parse(full_tunnel_config())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	endpoint := netip.MustParseAddrPort("203.0.113.5:51080")
	got, err := config.Uapi_set([]netip.AddrPort{endpoint})
	if err != nil {
		t.Fatalf("Uapi_set: %v", err)
	}

	want := "" +
		"private_key=31581ed671437915653ec6fd1a6b485ee2e5a287b09d1a9aabd097b485cede51\n" +
		"replace_peers=true\n" +
		"public_key=8e9737bfbfaf05849d1f2bcfeb5cc3d990bfaac6d4cb0df468a5ba4c037a0b48\n" +
		"preshared_key=3f89ce79559e3c73a2341dc34d43aab3a2d41b13be2d7cec66a48cbdd7bcb980\n" +
		"endpoint=203.0.113.5:51080\n" +
		"persistent_keepalive_interval=15\n" +
		"replace_allowed_ips=true\n" +
		"allowed_ip=0.0.0.0/0\n"

	if got != want {
		t.Fatalf("Uapi_set() =\n%q\nwant\n%q", got, want)
	}
}

func Test_uapi_set_omits_preshared_key_and_keepalive_when_absent(t *testing.T) {
	config_text := "[Interface]\n" +
		"PrivateKey = " + private_key_full + "\n" +
		"Address = 10.0.0.2/32\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = " + public_key_full + "\n" +
		"Endpoint = vpn.example.org:51080\n" +
		"AllowedIPs = 0.0.0.0/0\n"

	config, err := Parse(config_text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	got, err := config.Uapi_set([]netip.AddrPort{netip.MustParseAddrPort("203.0.113.5:51080")})
	if err != nil {
		t.Fatalf("Uapi_set: %v", err)
	}
	if strings.Contains(got, "preshared_key=") {
		t.Fatalf("Uapi_set() = %q, must not contain preshared_key with none configured", got)
	}
	if strings.Contains(got, "persistent_keepalive_interval=") {
		t.Fatalf("Uapi_set() = %q, must not contain persistent_keepalive_interval when zero", got)
	}
}

func Test_uapi_set_refuses_a_mismatched_number_of_endpoints(t *testing.T) {
	config, err := Parse(full_tunnel_config())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if _, err := config.Uapi_set(nil); err == nil {
		t.Fatal("expected an error for zero endpoints against one peer")
	}
	two := []netip.AddrPort{
		netip.MustParseAddrPort("203.0.113.5:51080"),
		netip.MustParseAddrPort("203.0.113.6:51081"),
	}
	if _, err := config.Uapi_set(two); err == nil {
		t.Fatal("expected an error for two endpoints against one peer")
	}
}

// Two peers each open their own block with public_key: splitting the output
// on "public_key=" must leave the device-level header as the only thing
// before the first split, proving public_key is the first line of every
// peer block, not merely present somewhere in it.
func Test_uapi_set_with_two_peers_opens_each_block_with_public_key(t *testing.T) {
	config_text := "[Interface]\n" +
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

	config, err := Parse(config_text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	endpoints := []netip.AddrPort{
		netip.MustParseAddrPort("203.0.113.5:51080"),
		netip.MustParseAddrPort("203.0.113.6:51081"),
	}
	got, err := config.Uapi_set(endpoints)
	if err != nil {
		t.Fatalf("Uapi_set: %v", err)
	}

	parts := strings.Split(got, "public_key=")
	if len(parts) != 3 {
		t.Fatalf("Uapi_set() has %d public_key= occurrences, want 2", len(parts)-1)
	}
	want_header := "private_key=31581ed671437915653ec6fd1a6b485ee2e5a287b09d1a9aabd097b485cede51\nreplace_peers=true\n"
	if parts[0] != want_header {
		t.Fatalf("device header = %q, want %q (nothing must precede the first public_key line except the device header)", parts[0], want_header)
	}
}
