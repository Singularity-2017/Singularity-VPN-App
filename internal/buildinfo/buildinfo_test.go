package buildinfo

import (
	"net"
	"net/url"
	"strings"
	"testing"
)

// The address ranges RFC 5737 sets aside for documentation. An address inside
// one of them routes nowhere, which is the point: it cannot be a real
// installation that somebody committed by accident.
var documentation_ranges = []string{
	"192.0.2.0/24",
	"198.51.100.0/24",
	"203.0.113.0/24",
}

// This test is the repository boundary rule made mechanical. The default
// control plane address is the one value in this repository that somebody
// might replace with a real machine while debugging and then commit.
func Test_default_control_plane_is_a_documentation_address(t *testing.T) {
	u, err := url.Parse(Control_plane)
	if err != nil {
		t.Fatalf("control plane address does not parse: %v", err)
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil {
		t.Fatalf("control plane host %q is not a literal address; a name could "+
			"resolve to a real machine", u.Hostname())
	}
	for _, r := range documentation_ranges {
		_, block, err := net.ParseCIDR(r)
		if err != nil {
			t.Fatalf("bad range in the test itself: %v", err)
		}
		if block.Contains(ip) {
			return
		}
	}
	t.Fatalf("control plane address %s is outside the RFC 5737 documentation "+
		"ranges %s -- a real address must never be committed",
		ip, strings.Join(documentation_ranges, ", "))
}

// Nothing the build did not supply may appear in the summary, because a
// version somebody can read is either a real version or absent.
func Test_summary_names_only_what_the_build_supplied(t *testing.T) {
	Version, Commit, Built_at = "", "", ""
	got := Summary()
	if strings.ContainsAny(got, "()") || strings.Contains(got, "built ") {
		t.Fatalf("summary invents a field the build did not supply: %q", got)
	}

	Version, Commit, Built_at = "1.2.3", "abc1234", "2026-09-16T00:00:00Z"
	got = Summary()
	for _, want := range []string{"1.2.3", "abc1234", "2026-09-16T00:00:00Z"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary drops %q: %q", want, got)
		}
	}
}
