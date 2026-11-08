// Package vless implements the bundled Xray native full-machine TUN backend.
package vless

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

const Name = "vless"
const tun_ipv4 = "172.31.255.1"

var Err_config = errors.New("vless: invalid or unsupported VLESS REALITY TCP configuration")

// Uri contains credentials. Its String and GoString deliberately redact them.
type Uri struct {
	host                                         string
	port                                         uint16
	id, sni, key, sid, fingerprint, flow, spider string
}

func (Uri) String() string     { return "VLESS configuration (redacted)" }
func (u Uri) GoString() string { return u.String() }

// Parse_uri accepts a deliberately small transport contract, never arbitrary
// Xray JSON, file paths, commands, insecure TLS options or duplicate parameters.
// Errors must not wrap net/url's error, which includes the original URI.
func Parse_uri(data []byte) (Uri, error) {
	bad := func() (Uri, error) { return Uri{}, Err_config }
	raw := string(data)
	if len(raw) > 8192 || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return bad()
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "vless" || u.Opaque != "" || u.User == nil || u.Path != "" || u.Hostname() == "" {
		return bad()
	}
	if _, ok := u.User.Password(); ok {
		return bad()
	}
	id, err := uuid.Parse(u.User.Username())
	if err != nil || id == uuid.Nil || id.String() != strings.ToLower(u.User.Username()) {
		return bad()
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil || port == 0 {
		return bad()
	}
	host := u.Hostname()
	if strings.ContainsAny(host, "%/\\") || !valid_host(host) {
		return bad()
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return bad()
	}
	allowed := map[string]bool{"type": true, "security": true, "encryption": true, "flow": true, "sni": true, "fp": true, "pbk": true, "sid": true, "spx": true, "headerType": true}
	for k, vs := range q {
		if !allowed[k] || len(vs) != 1 || strings.IndexFunc(vs[0], unicode.IsControl) >= 0 {
			return bad()
		}
	}
	if q.Get("security") != "reality" || (q.Get("type") != "" && q.Get("type") != "tcp" && q.Get("type") != "raw") || (q.Get("encryption") != "" && q.Get("encryption") != "none") || (q.Get("headerType") != "" && q.Get("headerType") != "none") {
		return bad()
	}
	flow := q.Get("flow")
	if flow != "" && flow != "xtls-rprx-vision" {
		return bad()
	}
	sni := q.Get("sni")
	if sni == "" || !valid_host(sni) || net.ParseIP(sni) != nil {
		return bad()
	}
	key, err := base64.RawURLEncoding.DecodeString(q.Get("pbk"))
	if err != nil || len(key) != 32 {
		return bad()
	}
	sid, err := hex.DecodeString(q.Get("sid"))
	if err != nil || len(sid) > 8 {
		return bad()
	}
	fp := q.Get("fp")
	if fp == "" {
		fp = "safari"
	}
	switch fp {
	case "safari", "chrome", "firefox", "edge", "ios", "android":
	default:
		return bad()
	}
	spider := q.Get("spx")
	if spider != "" && (!strings.HasPrefix(spider, "/") || len(spider) > 1024) {
		return bad()
	}
	return Uri{host: host, port: uint16(port), id: id.String(), sni: sni, key: q.Get("pbk"), sid: q.Get("sid"), fingerprint: fp, flow: flow, spider: spider}, nil
}
func valid_host(host string) bool {
	if a, e := netip.ParseAddr(host); e == nil {
		return a.Zone() == "" && !a.IsUnspecified() && !a.IsMulticast() && !a.IsLoopback()
	}
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// routes_except partitions both address families, leaving the exact transport
// address on its original route. No physical route is added or later removed.
func routes_except(endpoint netip.Addr) []netip.Prefix {
	var out []netip.Prefix
	var visit func(netip.Prefix)
	visit = func(p netip.Prefix) {
		if !p.Contains(endpoint) {
			out = append(out, p)
			return
		}
		if p.Bits() == endpoint.BitLen() {
			return
		}
		bits := p.Bits() + 1
		b := p.Addr().AsSlice()
		pos := bits - 1
		b[pos/8] |= 1 << uint(7-pos%8)
		other, _ := netip.AddrFromSlice(b)
		visit(netip.PrefixFrom(p.Addr(), bits))
		visit(netip.PrefixFrom(other, bits))
	}
	visit(netip.MustParsePrefix("0.0.0.0/0"))
	visit(netip.MustParsePrefix("::/0"))
	return out
}
func (u Uri) native_config(endpoint netip.Addr, name string) ([]byte, error) {
	routes := []string{}
	for _, p := range routes_except(endpoint) {
		routes = append(routes, p.String())
	}
	type object = map[string]any
	return json.Marshal(object{
		"log":       object{"loglevel": "warning"},
		"dns":       object{"servers": []string{"https://1.1.1.1/dns-query"}, "queryStrategy": "UseIPv4"},
		"inbounds":  []any{object{"tag": "tun", "protocol": "tun", "settings": object{"name": name, "desc": "Singularity", "mtu": 1500, "gateway": []string{tun_ipv4 + "/30", "fd31:5356::1/126"}, "dns": []string{"172.31.255.2"}, "autoSystemRoutingTable": routes, "autoOutboundsInterface": "", "autoSystemWfpBlockLeak": []string{"dns"}}}},
		"outbounds": []any{object{"tag": "proxy", "protocol": "vless", "settings": object{"vnext": []any{object{"address": endpoint.String(), "port": u.port, "users": []any{object{"id": u.id, "encryption": "none", "flow": u.flow}}}}}, "streamSettings": object{"network": "raw", "security": "reality", "realitySettings": object{"serverName": u.sni, "fingerprint": u.fingerprint, "publicKey": u.key, "shortId": u.sid, "spiderX": u.spider}}}, object{"tag": "dns", "protocol": "dns"}, object{"tag": "block", "protocol": "blackhole"}},
		"routing":   object{"rules": []any{object{"type": "field", "inboundTag": []string{"tun"}, "port": "53", "outboundTag": "dns"}, object{"type": "field", "ip": []string{"::/0"}, "outboundTag": "block"}}},
	})
}
