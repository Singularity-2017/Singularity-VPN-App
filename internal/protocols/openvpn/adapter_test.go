package openvpn

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"singularity-vpn-app/internal/protocols"
)

const a_profile = `client
nobind
remote 192.0.2.10 50080 udp4
remote 192.0.2.10 508 tcp4
server-poll-timeout 10
remote-cert-tls server
dev tun
cipher AES-128-GCM
setenv FRIENDLY_NAME "VPN node [client]"

<ca>
-----BEGIN CERTIFICATE-----
up /bin/sh
-----END CERTIFICATE-----
</ca>
<cert>
cert-body
</cert>
<key>
key-body
</key>
`

func Test_the_nodes_profile_is_accepted(t *testing.T) {
	if err := check_profile([]byte(a_profile)); err != nil {
		t.Fatalf("check_profile(the node's own template) = %v, want nil", err)
	}
}

func Test_a_profile_asking_for_more_than_a_tunnel_is_refused_by_the_option(t *testing.T) {
	cases := map[string]string{
		"up":              "up C:\\evil.cmd",
		"--down":          "--down C:\\evil.cmd",
		"plugin":          "plugin C:\\evil.dll",
		"script-security": "script-security 3",
		"management":      "management 127.0.0.1 7505",
		"log":             "log C:\\Windows\\System32\\x",
		"dev-node":        "dev-node Ethernet",
		"auth-user-pass":  "auth-user-pass",
		"cert":            "cert C:\\Users\\someone\\secret.pem",
	}
	for name, line := range cases {
		err := check_profile([]byte(strings.Replace(a_profile, "nobind\n", "nobind\n"+line+"\n", 1)))
		if !errors.Is(err, Err_profile_refused) || !strings.Contains(err.Error(), strings.TrimLeft(name, "-")) {
			t.Errorf("a profile with %q: %v, want a refusal naming %q", line, err, name)
		}
	}
}

func Test_an_option_is_read_the_way_openvpn_reads_it(t *testing.T) {
	cases := map[string]string{
		"a quoted name":         `"up" "cmd.exe /c x"`,
		"a single-quoted name":  `'script-security' 2`,
		"an escaped name":       `u\p cmd`,
		"setenv opt":            `setenv opt up cmd`,
		"an unlisted option":    `win-sys C:\evil`,
		"a module":              `pkcs11-providers C:\evil.dll`,
		"a quoted ca path":      `"ca" C:\any\file`,
		"ignore-unknown-option": `ignore-unknown-option up`,
	}
	for name, line := range cases {
		profile := strings.Replace(a_profile, "nobind\n", "nobind\n"+line+"\n", 1)
		if err := check_profile([]byte(profile)); !errors.Is(err, Err_profile_refused) {
			t.Errorf("%s (%s): %v, want refused", name, line, err)
		}
	}
	if got := tokens(`setenv FRIENDLY_NAME "VPN node [client]" # comment`); len(got) != 3 || got[2] != "VPN node [client]" {
		t.Fatalf("tokens = %q, want three, the quoted one whole", got)
	}
}

func Test_a_block_openvpn_would_end_early_is_refused(t *testing.T) {
	profile := strings.Replace(a_profile, "<cert>\ncert-body\n</cert>",
		"<cert>\ncert-body\n</cert> x\nup cmd\n</cert>", 1)
	if err := check_profile([]byte(profile)); !errors.Is(err, Err_profile_refused) {
		t.Fatalf("a </cert> with a tail: %v, want refused", err)
	}
}

func Test_an_option_inside_a_connection_block_is_checked_too(t *testing.T) {
	profile := strings.Replace(a_profile, "nobind\n", "nobind\n<connection>\nremote 192.0.2.11 443 tcp\nup C:\\evil.cmd\n</connection>\n", 1)
	if err := check_profile([]byte(profile)); !errors.Is(err, Err_profile_refused) {
		t.Fatalf("an up inside <connection>: %v, want refused", err)
	}
}

func Test_a_profile_that_cannot_run_here_is_refused(t *testing.T) {
	cases := map[string]string{
		"no remote":        strings.ReplaceAll(strings.ReplaceAll(a_profile, "remote 192.0.2.10 50080 udp4\n", ""), "remote 192.0.2.10 508 tcp4\n", ""),
		"no ca":            strings.Replace(a_profile, "<ca>\n-----BEGIN CERTIFICATE-----\nup /bin/sh\n-----END CERTIFICATE-----\n</ca>\n", "", 1),
		"tap":              strings.Replace(a_profile, "dev tun", "dev tap", 1),
		"unclosed block":   strings.Replace(a_profile, "</key>\n", "", 1),
		"an unknown block": strings.Replace(a_profile, "<key>", "<auth-user-pass>\nu\np\n</auth-user-pass>\n<key>", 1),
	}
	for name, profile := range cases {
		if err := check_profile([]byte(profile)); !errors.Is(err, Err_profile_refused) {
			t.Errorf("%s: %v, want refused", name, err)
		}
	}
}

func Test_the_log_tells_how_far_the_node_was_reached(t *testing.T) {
	var p progress
	steps := []struct {
		line string
		want string
	}{
		{"UDPv4 link remote: [AF_INET]192.0.2.10:50080", "no answer from the node over UDP"},
		{"TCP connection established with [AF_INET]192.0.2.10:508", "no answer from the node over TCP"},
		{"TLS: Initial packet from [AF_INET]192.0.2.10:508, sid=1 2", "the node answered over TCP; TLS did not finish"},
		{"[node] Peer Connection Initiated with [AF_INET]192.0.2.10:508", "TLS finished over TCP; the tunnel was not set up"},
		{"Initialization Sequence Completed", "the tunnel is set up over TCP"},
	}
	for _, step := range steps {
		p.read(step.line)
		if got := p.report(); got != step.want {
			t.Fatalf("after %q: report = %q, want %q", step.line, got, step.want)
		}
	}
}

func Test_a_kept_line_carries_no_address(t *testing.T) {
	got := redact("AUTH_FAILED from [AF_INET]192.0.2.10:508; route 198.51.100.7 setenv FRIENDLY_NAME \"VPN 192.0.2.10\"")
	if strings.Contains(got, "192.0.2") || strings.Contains(got, "198.51") {
		t.Fatalf("redact = %q, want no address left", got)
	}
}

// fake_process is openvpn.exe as a script of log lines.
type fake_process struct {
	lines chan string
	done  chan struct{}

	mu      sync.Mutex
	stopped bool
	killed  bool
	ignore  bool // does not exit when asked to stop
	once    sync.Once
}

func new_fake_process(lines ...string) *fake_process {
	p := &fake_process{lines: make(chan string, len(lines)+1), done: make(chan struct{})}
	for _, line := range lines {
		p.lines <- line
	}
	return p
}

func (p *fake_process) Lines() <-chan string  { return p.lines }
func (p *fake_process) Done() <-chan struct{} { return p.done }
func (p *fake_process) exit() {
	p.once.Do(func() { close(p.lines); close(p.done) })
}
func (p *fake_process) Stop() {
	p.mu.Lock()
	p.stopped = true
	ignore := p.ignore
	p.mu.Unlock()
	if !ignore {
		p.exit()
	}
}
func (p *fake_process) Kill() {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	p.exit()
}

type fake_device struct{ closed bool }

func (d *fake_device) Close() error { d.closed = true; return nil }

type harness struct {
	adapter *Adapter
	process *fake_process
	device  *fake_device
	args    []string
	profile string
}

func new_harness(t *testing.T, script *fake_process) *harness {
	h := &harness{process: script, device: &fake_device{}, profile: filepath.Join(t.TempDir(), "openvpn.ovpn")}
	h.adapter = New_adapter(func(args []string) (process, error) {
		h.args = args
		if _, err := os.Stat(h.profile); err != nil {
			t.Errorf("openvpn.exe started with no profile on disk: %v", err)
		}
		return h.process, nil
	}, func() (device, error) { return h.device, nil }, "Singularity OpenVPN", h.profile, nil)
	return h
}

func (h *harness) connect(ctx context.Context) error {
	return h.adapter.Connect(ctx, protocols.Connection_config{Node_id: "de-01", Protocol: Name, Data: []byte(a_profile)})
}

func (h *harness) assert_nothing_left(t *testing.T) {
	t.Helper()
	if !h.device.closed {
		t.Error("the adapter was left behind")
	}
	if _, err := os.Stat(h.profile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the profile, which holds the private key, was left on disk: %v", err)
	}
	if h.adapter.Status().Running {
		t.Error("Status says running")
	}
}

func Test_connect_returns_once_openvpn_reports_the_tunnel_set_up(t *testing.T) {
	h := new_harness(t, new_fake_process(
		"TCP connection established with [AF_INET]192.0.2.10:508",
		"Initialization Sequence Completed"))
	if err := h.connect(context.Background()); err != nil {
		t.Fatalf("Connect = %v", err)
	}
	status := h.adapter.Status()
	if !status.Running || status.Detail != "over TCP" {
		t.Fatalf("Status = %+v, want running over TCP", status)
	}
	joined := strings.Join(h.args, " ")
	for _, want := range []string{"--config " + h.profile, "--windows-driver wintun", "--dev-node Singularity OpenVPN", "--connect-retry-max 1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("openvpn.exe arguments %q lack %q", joined, want)
		}
	}
	if err := h.connect(context.Background()); !errors.Is(err, Err_already_connected) {
		t.Fatalf("a second Connect = %v, want Err_already_connected", err)
	}

	if err := h.adapter.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect = %v", err)
	}
	if !h.process.stopped || h.process.killed {
		t.Fatal("Disconnect did not ask openvpn.exe to stop, or killed it outright")
	}
	h.assert_nothing_left(t)
}

func Test_a_fatal_line_fails_connect_and_leaves_nothing(t *testing.T) {
	h := new_harness(t, new_fake_process("AUTH_FAILED from [AF_INET]192.0.2.10:508"))
	err := h.connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "AUTH_FAILED") || strings.Contains(err.Error(), "192.0.2") {
		t.Fatalf("Connect = %v, want the fatal line with its address removed", err)
	}
	h.assert_nothing_left(t)
}

func Test_openvpn_exiting_before_the_tunnel_is_up_says_how_far_it_got(t *testing.T) {
	p := new_fake_process("UDPv4 link remote: [AF_INET]192.0.2.10:50080")
	h := new_harness(t, p)
	go func() { time.Sleep(10 * time.Millisecond); p.exit() }()
	err := h.connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no answer from the node over UDP") {
		t.Fatalf("Connect = %v, want how far it got", err)
	}
	h.assert_nothing_left(t)
}

func Test_connect_out_of_time_stops_openvpn_and_leaves_nothing(t *testing.T) {
	h := new_harness(t, new_fake_process("TLS: Initial packet from [AF_INET]192.0.2.10:508"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := h.connect(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "the node answered") {
		t.Fatalf("Connect = %v, want the deadline and how far it got", err)
	}
	if !h.process.stopped {
		t.Fatal("openvpn.exe was not asked to stop")
	}
	h.assert_nothing_left(t)
}

func Test_a_refused_profile_starts_nothing(t *testing.T) {
	h := new_harness(t, new_fake_process())
	err := h.adapter.Connect(context.Background(), protocols.Connection_config{Protocol: Name, Data: []byte("client\nup evil\n")})
	if !errors.Is(err, Err_profile_refused) || h.args != nil {
		t.Fatalf("Connect = %v, started = %v; want refused before anything starts", err, h.args != nil)
	}
}

func Test_openvpn_exiting_with_the_tunnel_up_releases_the_session(t *testing.T) {
	p := new_fake_process("Initialization Sequence Completed")
	h := new_harness(t, p)
	if err := h.connect(context.Background()); err != nil {
		t.Fatalf("Connect = %v", err)
	}
	p.exit()
	deadline := time.Now().Add(time.Second)
	for h.adapter.Status().Running && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	h.assert_nothing_left(t)
	if err := h.adapter.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect after the exit = %v, want nil", err)
	}
}

func Test_openvpn_needs_longer_than_the_default_to_connect(t *testing.T) {
	var p protocols.Protocol = New_adapter(nil, nil, "", "", nil)
	budgeter, ok := p.(protocols.Connect_budgeter)
	if !ok || budgeter.Connect_budget() <= 5*time.Second {
		t.Fatal("the adapter names no connect budget beyond the default 5s; UDP-then-TCP cannot fit in it")
	}
	if _, ok := p.(protocols.Transport_reporter); !ok {
		t.Fatal("the adapter is not a Transport_reporter")
	}
}
