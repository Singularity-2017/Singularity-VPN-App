//go:build windows

// Command svpn-probe is the console harness for stage 3: enough to parse a
// wg-quick config, see what it would do to the machine, and -- with
// administrator -- actually bring the tunnel up and tear it down again,
// without any of the state machine, the control plane or the service around
// it. It is meant for somebody standing at a machine whose networking is
// about to be changed by a program that has never run before, so it prints
// what it is doing and never prints a private key.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"singularity-vpn-app/internal/protocols"
	"singularity-vpn-app/internal/protocols/wireguard"
	"singularity-vpn-app/internal/routing"
)

const default_adapter_name = "Singularity"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "plan":
		run_plan(os.Args[2:])
	case "up":
		run_up(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  svpn-probe plan --config <file> [--adapter <name>]")
	fmt.Fprintln(os.Stderr, "  svpn-probe up   --config <file> [--adapter <name>]")
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "svpn-probe: "+format+"\n", args...)
	os.Exit(1)
}

// --- plan ------------------------------------------------------------

// run_plan parses config and prints the plan. It touches nothing on the
// machine, needs no administrator, and changes no state -- including the
// resolve step, which reaches out to DNS but writes nothing anywhere. This
// is meant to run first: it turns "did it even understand the file" into a
// question with an answer before anything privileged is attempted.
func run_plan(args []string) {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	config_path := fs.String("config", "", "path to a wg-quick configuration file")
	adapter_name := fs.String("adapter", default_adapter_name, "the adapter name this tunnel would use")
	fs.Parse(args)

	if *config_path == "" {
		fmt.Fprintln(os.Stderr, "svpn-probe plan: --config is required")
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	data, err := os.ReadFile(*config_path)
	if err != nil {
		fatalf("reading %s: %v", *config_path, err)
	}

	parsed, err := wireguard.Parse(string(data))
	if err != nil {
		fatalf("parsing config: %v", err)
	}
	if len(parsed.Peers) != 1 {
		fatalf("this harness (like the adapter it drives) supports exactly one peer; this config has %d", len(parsed.Peers))
	}
	peer := parsed.Peers[0]

	fmt.Printf("adapter name: %s\n", *adapter_name)
	fmt.Printf("addresses:    %s\n", join_prefixes(parsed.Addresses))
	fmt.Printf("mtu:          %d\n", parsed.Mtu)
	fmt.Printf("dns:          %s\n", join_addrs(parsed.Dns))
	fmt.Printf("peer:         %s\n", peer.Public_key.Hex())
	fmt.Printf("endpoint:     %s\n", peer.Endpoint)

	resolver := wireguard.Default_resolver{}
	endpoint, resolve_err := resolver.Resolve(ctx, peer.Endpoint)
	if resolve_err != nil {
		fmt.Printf("resolved to:  FAILED: %v\n", resolve_err)
		fmt.Println()
		fmt.Println("cannot compute a routing plan: the endpoint did not resolve.")
		return
	}
	fmt.Printf("resolved to:  %s\n", endpoint)

	plan, err := routing.Plan_for(routing.Tunnel{
		Addresses: parsed.Addresses,
		Allowed:   peer.Allowed_ips,
		Dns:       parsed.Dns,
		Mtu:       parsed.Mtu,
		Endpoint:  endpoint,
	})
	if err != nil {
		fmt.Println()
		fmt.Printf("cannot compute a routing plan: %v\n", err)
		return
	}

	fmt.Println()
	fmt.Printf("full tunnel:  %v\n", plan.Full_tunnel)
	fmt.Println("routes:")
	for _, route := range plan.Routes {
		via := "the interface that already reached it (pinned, so the tunnel does not carry its own transport)"
		if route.Through_tunnel {
			via = "tunnel"
		}
		fmt.Printf("  %-22s via %s\n", route.Destination, via)
	}
}

// --- up ----------------------------------------------------------------

// run_up brings the tunnel up, prints the status, waits for Ctrl+C, tears
// it down and prints what it tore down. Administrator is checked before
// anything else runs, because a syscall failing partway through is a worse
// way for a person to find out they were never going to be allowed to do
// this.
func run_up(args []string) {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	config_path := fs.String("config", "", "path to a wg-quick configuration file")
	adapter_name := fs.String("adapter", default_adapter_name, "the adapter name to create")
	fs.Parse(args)

	if *config_path == "" {
		fmt.Fprintln(os.Stderr, "svpn-probe up: --config is required")
		os.Exit(2)
	}

	if !windows.GetCurrentProcessToken().IsElevated() {
		fmt.Fprintln(os.Stderr, "svpn-probe up: this needs Administrator -- it creates a network adapter and changes routes and DNS. Re-run from an elevated prompt.")
		os.Exit(1)
	}

	data, err := os.ReadFile(*config_path)
	if err != nil {
		fatalf("reading %s: %v", *config_path, err)
	}

	adapter, err := wireguard.New_windows_adapter(*adapter_name)
	if err != nil {
		fatalf("building adapter: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	fmt.Println("bringing the tunnel up...")
	if err := adapter.Connect(ctx, protocols.Connection_config{
		Node_id:  "svpn-probe",
		Protocol: "wireguard",
		Data:     data,
	}); err != nil {
		fatalf("connect: %v", err)
	}

	status := adapter.Status()
	fmt.Printf("up. running=%v since=%s detail=%q\n", status.Running, status.Since.Format(time.RFC3339), status.Detail)
	fmt.Println("press Ctrl+C to tear down.")

	<-ctx.Done()

	fmt.Println()
	fmt.Println("interrupted -- tearing down...")
	teardown_ctx, teardown_cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer teardown_cancel()
	if err := adapter.Disconnect(teardown_ctx); err != nil {
		fmt.Fprintf(os.Stderr, "svpn-probe up: tearing down: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("torn down: the adapter's routes, addresses and DNS were cleared.")
}

// --- printing helpers --------------------------------------------------

func join_prefixes(prefixes []netip.Prefix) string {
	parts := make([]string, len(prefixes))
	for i, p := range prefixes {
		parts[i] = p.String()
	}
	return strings.Join(parts, ", ")
}

func join_addrs(addrs []netip.Addr) string {
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = a.String()
	}
	return strings.Join(parts, ", ")
}
