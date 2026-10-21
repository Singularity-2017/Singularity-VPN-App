package openvpn

import (
	"bufio"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Err_profile_refused is what Connect returns for a profile this adapter
// will not hand to openvpn.exe.
var Err_profile_refused = errors.New("openvpn: profile refused")

// allowed_options are the options a profile may carry: those a client
// profile for a plain tunnel uses, and nothing else. openvpn.exe runs as
// LocalSystem, and the options it has that run a program, load a module,
// or read or write a file of the profile's choosing grow with every
// release; a list of what to refuse would always be one release behind.
// Anything not here is refused, by name.
var allowed_options = map[string]bool{
	"client": true, "tls-client": true, "pull": true, "nobind": true,
	"remote": true, "proto": true, "port": true, "rport": true,
	"remote-random": true, "connect-retry": true, "connect-timeout": true,
	"server-poll-timeout": true, "resolv-retry": true,
	"dev": true, "dev-type": true, "topology": true, "tun-mtu": true,
	"mssfix": true, "fragment": true, "sndbuf": true, "rcvbuf": true, "fast-io": true,
	"remote-cert-tls": true, "verify-x509-name": true, "ns-cert-type": true,
	"cipher": true, "data-ciphers": true, "data-ciphers-fallback": true, "ncp-ciphers": true,
	"auth": true, "tls-version-min": true, "tls-version-max": true, "tls-cipher": true,
	"tls-ciphersuites": true, "tls-groups": true, "key-direction": true,
	"reneg-sec": true, "reneg-bytes": true, "hand-window": true, "tran-window": true,
	"keepalive": true, "ping": true, "ping-restart": true, "ping-exit": true,
	"explicit-exit-notify": true, "persist-key": true, "persist-tun": true,
	"comp-lzo": true, "compress": true, "allow-compression": true,
	"redirect-gateway": true, "route": true, "route-ipv6": true, "route-metric": true,
	"route-delay": true, "dhcp-option": true, "block-outside-dns": true,
	"register-dns": true, "block-ipv6": true, "pull-filter": true,
	"auth-nocache": true, "mute-replay-warnings": true, "mute": true, "verb": true,
	"float": true, "setenv": true,
	// Key material, only as [inline]: a path would have openvpn.exe read
	// any file on the machine.
	"ca": true, "cert": true, "key": true, "tls-auth": true, "tls-crypt": true,
	"tls-crypt-v2": true, "extra-certs": true,
}

// inline_options are the options whose value is key material: only
// [inline], the value carried in the profile's own <tag> block, is accepted.
var inline_options = map[string]bool{
	"ca": true, "cert": true, "key": true, "tls-auth": true, "tls-crypt": true,
	"tls-crypt-v2": true, "extra-certs": true,
}

// allowed_setenv are the variables setenv may set. "setenv opt ..." is not
// a variable at all: it hands openvpn.exe an option to take if it knows it,
// which would carry anything past this check.
var allowed_setenv = map[string]bool{"friendly_name": true}

// inline_blocks are the <tag> blocks a profile may carry: key material,
// and <connection>, whose body is options checked like the rest.
var inline_blocks = map[string]bool{
	"ca": true, "cert": true, "key": true, "tls-auth": true, "tls-crypt": true,
	"tls-crypt-v2": true, "extra-certs": true, "connection": true,
}

// tokens splits an option line the way openvpn.exe does: on white space,
// with double quotes, single quotes and backslash escapes grouping, and a
// # or ; that starts a token ending the line. Reading quoted names as
// openvpn.exe reads them is the point: "up" in quotes is still up.
func tokens(line string) []string {
	var (
		out     []string
		current strings.Builder
		in_word bool
		quote   rune
		escaped bool
	)
	for _, r := range line {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '\\':
			escaped, in_word = true, true
		case quote == '"':
			if r == '"' {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, in_word = r, true
		case r == ' ' || r == '\t' || r == '\r' || r == '\v' || r == '\f':
			if in_word {
				out = append(out, current.String())
				current.Reset()
				in_word = false
			}
		case (r == '#' || r == ';') && !in_word:
			return out
		default:
			current.WriteRune(r)
			in_word = true
		}
	}
	if in_word {
		out = append(out, current.String())
	}
	return out
}

// check_profile refuses a profile that is not one this adapter can bring
// up, or that asks openvpn.exe for more than a tunnel. The refusal names the
// option, never a value: a value can be key material.
func check_profile(data []byte) error {
	var (
		block      string
		has_remote bool
		has_ca     bool
		dev_tun    = true
	)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if block != "" && block != "connection" {
			// openvpn.exe ends a block at any line that starts with its
			// closing tag, whatever follows. A line this check would read
			// differently is refused rather than guessed at.
			if len(line) >= len(block)+2 && strings.EqualFold(line[:len(block)+2], "</"+block) {
				if !strings.EqualFold(line, "</"+block+">") {
					return fmt.Errorf("%w: a malformed </%s>", Err_profile_refused, block)
				}
				block = ""
			}
			continue
		}
		if strings.HasPrefix(line, "</") {
			if block == "connection" && strings.EqualFold(line, "</connection>") {
				block = ""
				continue
			}
			return fmt.Errorf("%w: a stray closing tag", Err_profile_refused)
		}
		if strings.HasPrefix(line, "<") {
			tag := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(line, "<"), ">"))
			if !inline_blocks[tag] || !strings.HasSuffix(line, ">") {
				return fmt.Errorf("%w: the inline block <%s>", Err_profile_refused, tag)
			}
			if block == "connection" && tag == "connection" {
				return fmt.Errorf("%w: a <connection> inside another", Err_profile_refused)
			}
			if tag == "ca" {
				has_ca = true
			}
			block = tag
			continue
		}
		fields := tokens(line)
		if len(fields) == 0 {
			continue
		}
		name := strings.ToLower(strings.TrimPrefix(fields[0], "--"))
		if !allowed_options[name] {
			return fmt.Errorf("%w: the option %q", Err_profile_refused, name)
		}
		if inline_options[name] && (len(fields) < 2 || !strings.EqualFold(fields[1], "[inline]")) {
			return fmt.Errorf("%w: the option %q names a file; the profile must carry it inline", Err_profile_refused, name)
		}
		switch name {
		case "setenv":
			if len(fields) < 2 || !allowed_setenv[strings.ToLower(fields[1])] {
				return fmt.Errorf("%w: setenv of anything but FRIENDLY_NAME", Err_profile_refused)
			}
		case "remote":
			has_remote = true
		case "dev":
			dev_tun = len(fields) > 1 && strings.HasPrefix(strings.ToLower(fields[1]), "tun")
		case "dev-type":
			dev_tun = len(fields) > 1 && strings.EqualFold(fields[1], "tun")
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%w: %v", Err_profile_refused, err)
	}
	switch {
	case block != "":
		return fmt.Errorf("%w: <%s> is never closed", Err_profile_refused, block)
	case !has_remote:
		return fmt.Errorf("%w: it names no server (no remote)", Err_profile_refused)
	case !has_ca:
		return fmt.Errorf("%w: it carries no <ca>", Err_profile_refused)
	case !dev_tun:
		// wintun carries IP packets only; a tap profile cannot run on it.
		return fmt.Errorf("%w: it asks for a tap device, and only tun is available", Err_profile_refused)
	}
	return nil
}

// phase is how far openvpn.exe got, read from its log.
type phase int

const (
	phase_starting phase = iota
	phase_trying         // a transport to the node is being opened
	phase_answered       // the node answered: the first TLS packet came back
	phase_secured        // TLS finished: the node accepted this client
	phase_up             // routes and addresses are in place
)

// progress is what the log has told so far.
type progress struct {
	phase     phase
	transport string // "UDP" or "TCP", once one was tried
	fatal     string // the line that ended the attempt, addresses removed
	carried   carried
}

// fatal_markers end an attempt for good: openvpn.exe exits after them, or
// waits forever for something that will not come.
var fatal_markers = []string{
	"AUTH_FAILED",
	"Exiting due to fatal error",
	"Options error:",
	"All wintun adapters on this system are currently in use",
	"Cannot open TUN/TAP dev",
	"Cannot load inline certificate",
	"Cannot load CA certificate",
	"Cannot load private key",
	"ERROR: Windows route add command failed",
}

// read takes one log line into account.
func (p *progress) read(line string) {
	p.carried.read_push(line)
	switch {
	case strings.Contains(line, "Initialization Sequence Completed"):
		p.phase = phase_up
	case strings.Contains(line, "Peer Connection Initiated with"):
		p.advance(phase_secured)
	case strings.Contains(line, "TLS: Initial packet from"):
		p.advance(phase_answered)
	case strings.Contains(line, "TCP connection established with"),
		strings.Contains(line, "Attempting to establish TCP connection with"),
		strings.Contains(line, "TCP_CLIENT link remote"):
		p.transport = "TCP"
		p.advance(phase_trying)
	case strings.Contains(line, "UDPv4 link remote"),
		strings.Contains(line, "UDPv6 link remote"),
		strings.Contains(line, "UDP link remote"):
		p.transport = "UDP"
		p.advance(phase_trying)
	}
	for _, marker := range fatal_markers {
		if strings.Contains(line, marker) {
			p.fatal = redact(line)
			return
		}
	}
}

// advance moves forward only. After a failed transport openvpn.exe tries the
// next one from the beginning, so a phase can be reached again; what was
// once reached on another transport is no longer true, though, and the
// transport switch resets it.
func (p *progress) advance(to phase) {
	if to == phase_trying {
		p.phase = phase_trying
		return
	}
	if to > p.phase {
		p.phase = to
	}
}

// report is the transport report: how far the node was reached, and over
// what. No address, no name.
func (p progress) report() string {
	over := ""
	if p.transport != "" {
		over = " over " + p.transport
	}
	switch p.phase {
	case phase_trying:
		return "no answer from the node" + over
	case phase_answered:
		return "the node answered" + over + "; TLS did not finish"
	case phase_secured:
		return "TLS finished" + over + "; the tunnel was not set up"
	case phase_up:
		return "the tunnel is set up" + over
	}
	return ""
}

var (
	ipv4_pattern     = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	ipv6_pattern     = regexp.MustCompile(`(?i)\b[0-9a-f]{0,4}(?::[0-9a-f]{0,4}){2,7}\b`)
	af_inet_pattern  = regexp.MustCompile(`\[AF_INET6?\]\S+`)
	resolve_pattern  = regexp.MustCompile(`(?i)(RESOLVE: [^:]*: )\S+`)
	path_pattern     = regexp.MustCompile(`(?i)\b[a-z]:\\\S*`)
	friendly_pattern = regexp.MustCompile(`(?i)FRIENDLY_NAME.*`)
)

// redact removes addresses, host names and paths from a log line before it
// is kept: the node's address is not written anywhere a person could copy
// it from.
func redact(line string) string {
	line = af_inet_pattern.ReplaceAllString(line, "[address]")
	line = resolve_pattern.ReplaceAllString(line, "${1}[host]")
	line = ipv4_pattern.ReplaceAllString(line, "[address]")
	line = ipv6_pattern.ReplaceAllString(line, "[address]")
	line = path_pattern.ReplaceAllString(line, "[path]")
	return friendly_pattern.ReplaceAllString(line, "FRIENDLY_NAME [removed]")
}
