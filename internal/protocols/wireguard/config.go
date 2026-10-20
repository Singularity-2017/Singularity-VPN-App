package wireguard

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Tunnel_config is a parsed wg-quick configuration: the local interface and
// every peer it will talk to. Parse is the only way to build one, so a
// Tunnel_config that exists at all has already passed every refusal below.
type Tunnel_config struct {
	Private_key Key
	Addresses   []netip.Prefix
	Dns         []netip.Addr
	Mtu         int
	Peers       []Peer
	// Obfuscation is the AmneziaWG parameters of the [Interface] section,
	// in the order the config gave them, keys lower-cased. Empty for plain
	// WireGuard; the device then speaks plain WireGuard.
	Obfuscation []Obfuscation_param
}

// Obfuscation_param is one AmneziaWG parameter, passed to the device as it
// came. The values are not secret: they shape packets, they do not key them.
type Obfuscation_param struct {
	Key   string
	Value string
}

// Peer is one [Peer] section of a wg-quick configuration.
type Peer struct {
	Public_key    Key
	Preshared_key Key // zero when the config carried none
	Has_preshared bool
	Allowed_ips   []netip.Prefix
	// Endpoint is "host:port", deliberately unresolved: resolving a hostname
	// is a network call and this package is pure. It is resolved exactly
	// once, by the caller, before the tunnel comes up -- see the comment on
	// Uapi_set for why it cannot happen here or be deferred until after the
	// tunnel is already running.
	Endpoint  string
	Keepalive time.Duration
}

// Err_incomplete_config is what Parse returns when the text parsed cleanly
// but arrived without a value this client acts on -- the wg-quick analogue
// of api.Err_incomplete_response, for the same reason: Go cannot tell a
// field that was never set apart from one deliberately left empty.
var Err_incomplete_config = errors.New("wireguard: config is incomplete")

// Err_unsupported_directive is what Parse returns for a directive this
// client will not honour.
//
// PostUp, PostDown, PreUp and PreDown are shell commands. The service that
// will eventually run this parser runs as LocalSystem. A config arrives over
// the network; a client that executed those lines would be a remote command
// execution hole with a VPN wrapped around it, and one that silently
// dropped them would give a person a tunnel the server believes is
// configured and which is not. Neither is acceptable, so this refuses and
// says which line it refused.
//
// Table, SaveConfig and FwMark are Linux-specific, meaningless on Windows,
// and each changes routing in a way this client cannot reproduce.
//
// Any other key this parser does not recognise, in either section, falls
// here too: wg-quick itself errors on an unknown key, and an unrecognised
// key in a tunnel configuration means the tunnel will not behave the way
// whoever wrote the config intended. There is no such thing as a harmless
// surprise in a routing configuration.
var Err_unsupported_directive = errors.New("wireguard: config carries a directive this client will not honour")

// Err_obfuscation_unsupported is what Parse returns for an AmneziaWG
// parameter the embedded library (amneziawg-go, AmneziaWG 1.5) does not
// speak -- S3 and S4 of a newer protocol version. The parameters it does
// speak are kept in Tunnel_config.Obfuscation and handed to the device.
//
// This is deliberately a different sentinel from Err_unsupported_directive
// because the consequence is different and the message must be too. These
// parameters are traffic obfuscation. A client that stripped one and
// connected anyway would produce a tunnel that either does not come up at
// all, or -- worse -- comes up carrying recognisable traffic for somebody
// whose reason for using an obfuscated protocol was that recognisable
// traffic is a problem where they live.
var Err_obfuscation_unsupported = errors.New("wireguard: node offers an obfuscated protocol this build cannot speak")

// required_field and check_required are copied from
// internal/api/errors.go (required_field / check_required) rather than
// exported from that package: they are twelve lines, and the two packages
// have no business depending on each other for something this small.
type required_field struct {
	name    string
	arrived bool
}

// check_required reports every missing field at once rather than the first
// one, because a config that lacks one required value often lacks several,
// and one refusal per round of discovery is a slow way to find that out.
func check_required(what string, fields ...required_field) error {
	var missing []string
	for _, field := range fields {
		if !field.arrived {
			missing = append(missing, field.name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s arrived without %s",
		Err_incomplete_config, what, strings.Join(missing, ", "))
}

// shell_directives are PostUp/PostDown/PreUp/PreDown. See the comment on
// Err_unsupported_directive.
var shell_directives = map[string]bool{
	"postup":   true,
	"postdown": true,
	"preup":    true,
	"predown":  true,
}

// linux_only_directives are meaningless on Windows and each changes routing
// in a way this client cannot reproduce.
var linux_only_directives = map[string]bool{
	"table":      true,
	"saveconfig": true,
	"fwmark":     true,
}

// obfuscation_keys are the AmneziaWG 1.5 parameters amneziawg-go v1.0.4
// accepts. A true value is a number; false is a packet template passed as
// text (I1-I5, J1-J3).
var obfuscation_keys = map[string]bool{
	"jc": true, "jmin": true, "jmax": true,
	"s1": true, "s2": true,
	"h1": true, "h2": true, "h3": true, "h4": true,
	"i1": false, "i2": false, "i3": false, "i4": false, "i5": false,
	"j1": false, "j2": false, "j3": false,
	"itime": true,
}

// unspoken_obfuscation_keys are AmneziaWG parameters of a newer protocol
// version than the embedded library speaks.
var unspoken_obfuscation_keys = map[string]bool{"s3": true, "s4": true}

// raw_interface holds the [Interface] section exactly as text, before any
// field is parsed into its typed form -- so that a required-field check can
// run once, over the whole document, before any value is interpreted.
type raw_interface struct {
	private_key     string
	has_private_key bool
	// addresses and dns accumulate. wg-quick lets Address, DNS and
	// AllowedIPs appear on several lines and adds them up, which is how a
	// dual-stack config is written; letting the last line win instead drops
	// the other family's address, and half the routed networks, without
	// saying anything. A tunnel that comes up looking healthy and carries
	// less than it was told to is the failure this whole package exists to
	// refuse.
	addresses []string
	dns       []string
	mtu       string
	has_mtu   bool
}

// raw_peer is one [Peer] section, held the same way as raw_interface and for
// the same reason.
type raw_peer struct {
	public_key     string
	has_public_key bool
	preshared_key  string
	has_preshared  bool
	endpoint       string
	has_endpoint   bool
	allowed_ips    []string // accumulates, see raw_interface
	keepalive      string
	has_keepalive  bool
}

// set_once stores a value for a key that names exactly one thing. Such a key
// appearing twice is not a config anybody meant to write, and choosing one of
// the two would be guessing about the private key, the peer or the endpoint --
// the values the tunnel's identity rests on.
func set_once(field *string, seen *bool, key, value string) error {
	if *seen {
		return fmt.Errorf("wireguard: %s appears more than once", key)
	}
	*field, *seen = value, true
	return nil
}

// Parse reads a wg-quick configuration text and returns the Tunnel_config it
// describes, or refuses with Err_incomplete_config, Err_unsupported_directive
// or Err_obfuscation_unsupported.
func Parse(text string) (Tunnel_config, error) {
	const (
		section_none = iota
		section_interface
		section_peer
	)

	section := section_none
	interface_seen := false
	var iface raw_interface
	var peers []*raw_peer
	var obfuscation []Obfuscation_param

	for _, raw_line := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw_line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			switch name {
			case "interface":
				if interface_seen {
					return Tunnel_config{}, fmt.Errorf("wireguard: more than one [Interface] section")
				}
				interface_seen = true
				section = section_interface
			case "peer":
				peers = append(peers, &raw_peer{})
				section = section_peer
			default:
				return Tunnel_config{}, fmt.Errorf("wireguard: unknown section %q", line)
			}
			continue
		}

		if section == section_none {
			return Tunnel_config{}, fmt.Errorf("wireguard: %q appears before any [Interface] or [Peer] section", line)
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return Tunnel_config{}, fmt.Errorf("wireguard: %q is not a key = value line", line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		lower_key := strings.ToLower(key)

		if unspoken_obfuscation_keys[lower_key] {
			return Tunnel_config{}, fmt.Errorf("%w: this node offers a newer AmneziaWG than this build speaks (%s)",
				Err_obfuscation_unsupported, key)
		}
		if numeric, ok := obfuscation_keys[lower_key]; ok {
			if section != section_interface {
				return Tunnel_config{}, fmt.Errorf("wireguard: %s belongs in [Interface], not [Peer]", key)
			}
			if err := check_obfuscation(lower_key, value, numeric); err != nil {
				return Tunnel_config{}, err
			}
			for _, seen := range obfuscation {
				if seen.Key == lower_key {
					return Tunnel_config{}, fmt.Errorf("wireguard: %s appears twice", key)
				}
			}
			obfuscation = append(obfuscation, Obfuscation_param{Key: lower_key, Value: value})
			continue
		}
		if shell_directives[lower_key] || linux_only_directives[lower_key] {
			return Tunnel_config{}, fmt.Errorf("%w: %s", Err_unsupported_directive, key)
		}

		if section == section_interface {
			switch lower_key {
			case "privatekey":
				if err := set_once(&iface.private_key, &iface.has_private_key, key, value); err != nil {
					return Tunnel_config{}, err
				}
			case "address":
				iface.addresses = append(iface.addresses, value)
			case "dns":
				iface.dns = append(iface.dns, value)
			case "mtu":
				if err := set_once(&iface.mtu, &iface.has_mtu, key, value); err != nil {
					return Tunnel_config{}, err
				}
			default:
				return Tunnel_config{}, fmt.Errorf("%w: %s", Err_unsupported_directive, key)
			}
			continue
		}

		// section == section_peer
		peer := peers[len(peers)-1]
		switch lower_key {
		case "publickey":
			if err := set_once(&peer.public_key, &peer.has_public_key, key, value); err != nil {
				return Tunnel_config{}, err
			}
		case "presharedkey":
			if err := set_once(&peer.preshared_key, &peer.has_preshared, key, value); err != nil {
				return Tunnel_config{}, err
			}
		case "endpoint":
			if err := set_once(&peer.endpoint, &peer.has_endpoint, key, value); err != nil {
				return Tunnel_config{}, err
			}
		case "allowedips":
			peer.allowed_ips = append(peer.allowed_ips, value)
		case "persistentkeepalive":
			if err := set_once(&peer.keepalive, &peer.has_keepalive, key, value); err != nil {
				return Tunnel_config{}, err
			}
		default:
			return Tunnel_config{}, fmt.Errorf("%w: %s", Err_unsupported_directive, key)
		}
	}

	required := []required_field{
		{"PrivateKey", iface.has_private_key},
		{"Address", len(iface.addresses) > 0},
		{"a [Peer] section", len(peers) > 0},
	}
	for i, peer := range peers {
		prefix := ""
		if len(peers) > 1 {
			prefix = fmt.Sprintf("peer %d ", i+1)
		}
		required = append(required,
			required_field{prefix + "PublicKey", peer.has_public_key},
			required_field{prefix + "Endpoint", peer.has_endpoint},
			required_field{prefix + "AllowedIPs", len(peer.allowed_ips) > 0},
		)
	}
	if err := check_required("the wg-quick config", required...); err != nil {
		return Tunnel_config{}, err
	}

	private_key, err := Parse_key(iface.private_key)
	if err != nil {
		return Tunnel_config{}, err
	}

	addresses, err := parse_prefix_list(iface.addresses)
	if err != nil {
		return Tunnel_config{}, err
	}

	var dns []netip.Addr
	if len(iface.dns) > 0 {
		dns, err = parse_dns_list(iface.dns)
		if err != nil {
			return Tunnel_config{}, err
		}
	}

	mtu := 1420
	if iface.has_mtu {
		mtu, err = strconv.Atoi(iface.mtu)
		if err != nil {
			return Tunnel_config{}, fmt.Errorf("wireguard: MTU %q is not a number", iface.mtu)
		}
	}

	built_peers := make([]Peer, 0, len(peers))
	for _, peer := range peers {
		public_key, err := Parse_key(peer.public_key)
		if err != nil {
			return Tunnel_config{}, err
		}

		var preshared_key Key
		if peer.has_preshared {
			preshared_key, err = Parse_key(peer.preshared_key)
			if err != nil {
				return Tunnel_config{}, err
			}
		}

		allowed_ips, err := parse_prefix_list(peer.allowed_ips)
		if err != nil {
			return Tunnel_config{}, err
		}

		if err := validate_endpoint(peer.endpoint); err != nil {
			return Tunnel_config{}, err
		}

		keepalive, err := parse_keepalive(peer.keepalive)
		if err != nil {
			return Tunnel_config{}, err
		}

		built_peers = append(built_peers, Peer{
			Public_key:    public_key,
			Preshared_key: preshared_key,
			Has_preshared: peer.has_preshared,
			Allowed_ips:   allowed_ips,
			Endpoint:      peer.endpoint,
			Keepalive:     keepalive,
		})
	}

	return Tunnel_config{
		Private_key: private_key,
		Addresses:   addresses,
		Dns:         dns,
		Mtu:         mtu,
		Peers:       built_peers,
		Obfuscation: obfuscation,
	}, nil
}

// check_obfuscation refuses a value the device would refuse later, where
// the error would no longer name the line: a number that is not one, or an
// empty template.
func check_obfuscation(key, value string, numeric bool) error {
	if numeric {
		if _, err := strconv.ParseUint(value, 10, 32); err != nil {
			return fmt.Errorf("wireguard: %s = %q is not a number", key, value)
		}
		return nil
	}
	if value == "" {
		return fmt.Errorf("wireguard: %s is empty", key)
	}
	return nil
}

// parse_prefix_list parses every line a key appeared on, each of them a
// comma-separated list of CIDR prefixes -- the form wg-quick uses for Address
// and AllowedIPs, and which it accumulates across repeated lines.
func parse_prefix_list(lines []string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, line := range lines {
		for _, part := range strings.Split(line, ",") {
			part = strings.TrimSpace(part)
			prefix, err := netip.ParsePrefix(part)
			if err != nil {
				return nil, fmt.Errorf("wireguard: %q is not a CIDR prefix: %w", part, err)
			}
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes, nil
}

// parse_dns_list parses a comma-separated list of resolver addresses.
// wg-quick also allows a search domain here, which this client refuses:
// this client sets resolvers on an adapter and has nowhere to put a search
// domain, and quietly dropping it would give a person a tunnel whose name
// resolution is subtly not what the server intended.
func parse_dns_list(lines []string) ([]netip.Addr, error) {
	var addrs []netip.Addr
	for _, line := range lines {
		for _, part := range strings.Split(line, ",") {
			part = strings.TrimSpace(part)
			addr, err := netip.ParseAddr(part)
			if err != nil {
				return nil, fmt.Errorf("wireguard: DNS value %q is not an IP address", part)
			}
			addrs = append(addrs, addr)
		}
	}
	return addrs, nil
}

// validate_endpoint checks that Endpoint splits into a host and a port in
// 1..65535, and leaves the host untouched: resolving it is a network call
// and this package is pure.
func validate_endpoint(text string) error {
	host, port_text, err := net.SplitHostPort(text)
	if err != nil {
		return fmt.Errorf("wireguard: endpoint %q is not host:port: %w", text, err)
	}
	if host == "" {
		return fmt.Errorf("wireguard: endpoint %q has no host", text)
	}
	port, err := strconv.Atoi(port_text)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("wireguard: endpoint %q has an invalid port", text)
	}
	return nil
}

// parse_keepalive parses PersistentKeepalive. "0" and "off" both mean no
// keepalive.
func parse_keepalive(text string) (time.Duration, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || strings.EqualFold(trimmed, "off") {
		return 0, nil
	}
	seconds, err := strconv.Atoi(trimmed)
	if err != nil || seconds < 0 {
		return 0, fmt.Errorf("wireguard: PersistentKeepalive %q is not a number of seconds or \"off\"", text)
	}
	return time.Duration(seconds) * time.Second, nil
}
