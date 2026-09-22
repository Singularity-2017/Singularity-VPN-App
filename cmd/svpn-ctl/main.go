//go:build windows

// Command svpn-ctl drives the service over its pipe from an ordinary,
// unelevated console. It is the stage's harness, standing where the
// interface will stand, and it must never need Administrator: a harness
// that needed elevation would prove nothing about the boundary it exists
// to demonstrate -- that everything privileged happens in the service, and
// an ordinary user can only ask for it through the pipe's closed command
// set.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"singularity-vpn-app/internal/ipc"
)

// call_timeout bounds one request. It is above connection.Config's 30s
// Connect_timeout, so a connect is given an answer by the service rather
// than being abandoned here a moment before it would have got one.
const call_timeout = 60 * time.Second

// Exit codes: 0 done, 1 the service refused or could not be reached, 2 the
// command line itself was wrong.
const (
	exit_refused = 1
	exit_usage   = 2
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exit_usage)
	}

	command, args := os.Args[1], os.Args[2:]
	var run func(ctx context.Context, client *ipc.Client) error
	switch command {
	case "status":
		no_args(command, args)
		run = run_status
	case "version":
		no_args(command, args)
		run = run_version
	case "connect":
		run = parse_connect(args)
	case "disconnect":
		no_args(command, args)
		run = func(ctx context.Context, client *ipc.Client) error {
			if err := client.Disconnect(ctx); err != nil {
				return err
			}
			fmt.Println("disconnected.")
			return nil
		}
	case "sign-in":
		run = parse_sign_in(args)
	case "sign-out":
		no_args(command, args)
		run = func(ctx context.Context, client *ipc.Client) error {
			if err := client.Sign_out(ctx); err != nil {
				return err
			}
			fmt.Println("signed out; the device token was removed from this machine.")
			return nil
		}
	case "events":
		no_args(command, args)
		run = run_events
	default:
		usage()
		os.Exit(exit_usage)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	conn, err := ipc.Dial(ctx)
	if err != nil {
		fail(err)
	}
	client := ipc.New_client(conn)
	defer client.Close()

	if err := run(ctx, client); err != nil {
		fail(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  svpn-ctl status")
	fmt.Fprintln(os.Stderr, "  svpn-ctl version")
	fmt.Fprintln(os.Stderr, "  svpn-ctl sign-in --username <name>     the password is asked for at the console")
	fmt.Fprintln(os.Stderr, "  svpn-ctl sign-out")
	fmt.Fprintln(os.Stderr, "  svpn-ctl connect                       the service chooses the node")
	fmt.Fprintln(os.Stderr, "  svpn-ctl connect --node <id> --protocol <name>")
	fmt.Fprintln(os.Stderr, "  svpn-ctl disconnect")
	fmt.Fprintln(os.Stderr, "  svpn-ctl events                        print events until Ctrl+C")
}

func usage_error(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "svpn-ctl: "+format+"\n", args...)
	os.Exit(exit_usage)
}

func no_args(command string, args []string) {
	if len(args) > 0 {
		usage_error("%s takes no arguments", command)
	}
}

// fail prints why and exits. A refusal from the service carries its own
// message; not_signed_in also says what to do about it, because that is
// the one refusal a person fixes with a command rather than by waiting.
func fail(err error) {
	fmt.Fprintf(os.Stderr, "svpn-ctl: %v\n", err)
	if errors.Is(err, ipc.Remote_not_signed_in) {
		fmt.Fprintln(os.Stderr, "sign in first: svpn-ctl sign-in --username <name>")
	}
	os.Exit(exit_refused)
}

func with_timeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, call_timeout)
}

func run_status(ctx context.Context, client *ipc.Client) error {
	ctx, cancel := with_timeout(ctx)
	defer cancel()
	status, err := client.Status(ctx)
	if err != nil {
		return err
	}

	signed_in := "no"
	if status.Signed_in {
		signed_in = "yes"
	}
	fmt.Printf("state:      %s\n", status.State)
	fmt.Printf("signed in:  %s\n", signed_in)
	// Each of these is printed only when the service reported it. An
	// absent field prints nothing at all -- not a dash, not a zero time:
	// a number shown is a real one or it is not shown.
	if status.Node_id != "" {
		fmt.Printf("node:       %s\n", status.Node_id)
	}
	if status.Protocol != "" {
		fmt.Printf("protocol:   %s\n", status.Protocol)
	}
	if status.Session_id != "" {
		fmt.Printf("session:    %s\n", status.Session_id)
	}
	if status.Since != nil {
		fmt.Printf("since:      %s\n", status.Since.Local().Format(time.DateTime))
	}
	return nil
}

func run_version(ctx context.Context, client *ipc.Client) error {
	ctx, cancel := with_timeout(ctx)
	defer cancel()
	version, err := client.Version(ctx)
	if err != nil {
		return err
	}
	if version.Version == "" && version.Commit == "" && version.Built == "" {
		fmt.Println("the service was built without version information.")
		return nil
	}
	if version.Version != "" {
		fmt.Printf("version:  %s\n", version.Version)
	}
	if version.Commit != "" {
		fmt.Printf("commit:   %s\n", version.Commit)
	}
	if version.Built != "" {
		fmt.Printf("built:    %s\n", version.Built)
	}
	return nil
}

func parse_connect(args []string) func(ctx context.Context, client *ipc.Client) error {
	flags := flag.NewFlagSet("connect", flag.ExitOnError)
	node := flags.String("node", "", "the node to connect to; needs --protocol")
	protocol := flags.String("protocol", "", "the protocol to connect with; needs --node")
	flags.Parse(args)
	if flags.NArg() > 0 {
		usage_error("connect: unexpected argument %q", flags.Arg(0))
	}

	params := ipc.Connect_params{Mode: ipc.Mode_auto}
	switch {
	case *node == "" && *protocol == "":
	case *node != "" && *protocol != "":
		params = ipc.Connect_params{Mode: ipc.Mode_manual, Node_id: *node, Protocol: *protocol}
	default:
		usage_error("connect: --node and --protocol go together; give both, or neither to let the service choose")
	}

	return func(ctx context.Context, client *ipc.Client) error {
		ctx, cancel := with_timeout(ctx)
		defer cancel()
		fmt.Println("connecting...")
		if err := client.Connect(ctx, params); err != nil {
			return err
		}
		return run_status(ctx, client)
	}
}

func parse_sign_in(args []string) func(ctx context.Context, client *ipc.Client) error {
	flags := flag.NewFlagSet("sign-in", flag.ExitOnError)
	username := flags.String("username", "", "the account's username")
	flags.Parse(args)
	if flags.NArg() > 0 {
		usage_error("sign-in: unexpected argument -- the password is never given on the command line; it is asked for")
	}
	if *username == "" {
		usage_error("sign-in: --username is required")
	}

	return func(ctx context.Context, client *ipc.Client) error {
		// Asked for after the pipe is open and trusted: Dial has already
		// checked who owns it, so nothing is typed for an impersonator.
		password, err := read_password("password: ")
		if err != nil {
			return err
		}
		ctx, cancel := with_timeout(ctx)
		defer cancel()
		result, err := client.Sign_in(ctx, ipc.Sign_in_params{Username: *username, Password: password})
		if err != nil {
			return err
		}
		fmt.Printf("signed in; this machine is device %s on the account.\n", result.Device_id)
		return nil
	}
}

func run_events(ctx context.Context, client *ipc.Client) error {
	channel, err := client.Subscribe(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "listening; Ctrl+C to stop.")
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-channel:
			if !ok {
				return errors.New("the service closed the connection")
			}
			line := event.At.Local().Format(time.DateTime) + "  " + event.Kind
			if event.Session_id != "" {
				line += "  session=" + event.Session_id
			}
			if len(event.Data) > 0 {
				line += "  " + string(event.Data)
			}
			fmt.Println(line)
		}
	}
}
