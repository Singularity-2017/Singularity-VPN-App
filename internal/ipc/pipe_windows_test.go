//go:build windows

package ipc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// These run against the real pipe namespace. Each steps aside when a real
// service already holds the name: a test must never interfere with a
// machine's running VPN.

// Test_a_squatted_pipe_is_refused is the squatting attack played out for
// real: this test process creates the service's pipe name the way an
// impersonator would -- through go-winio, without the O:BA that only a
// token holding Administrators may assign -- and then dials it.
//
// It runs elevated or not. Elevated, the squatter's pipe is owned by the
// creating user on a stock Windows install, which is exactly why Listen
// sets the owner explicitly. The one configuration where it would be owned
// by Administrators anyway is a machine whose policy makes the group the
// default owner; then the squatter is an administrator, who owns the
// machine already, and the test says so instead of failing.
func Test_a_squatted_pipe_is_refused(t *testing.T) {
	squatter, err := winio.ListenPipe(Pipe_name, &winio.PipeConfig{SecurityDescriptor: `D:P(A;;GA;;;WD)`})
	if err != nil {
		t.Skipf("the pipe name is taken, probably by a running service: %v", err)
	}
	defer squatter.Close()

	go func() {
		conn, err := squatter.Accept()
		if err == nil {
			conn.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx)
	if err == nil {
		defer conn.Close()
		// Skip only on the policy's say-so, read from the pipe itself --
		// not on being elevated, which would make this test pass whatever
		// Dial checked.
		descriptor, sd_err := windows.GetSecurityInfo(windows.Handle(conn.(interface{ Fd() uintptr }).Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
		if sd_err == nil {
			if owner, _, owner_err := descriptor.Owner(); owner_err == nil && owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
				t.Skip("this machine's policy makes Administrators the default owner of what an elevated process creates")
			}
		}
		t.Fatal("Dial trusted a pipe created without the service's owner -- that is the squatter a sign-in password would be sent to")
	}
	if !errors.Is(err, Err_untrusted_pipe) {
		t.Fatalf("Dial() = %v, want Err_untrusted_pipe", err)
	}
}

// Test_a_pipe_created_elevated_is_trusted_and_carries_the_protocol is the
// other side of the owner check, and the only proof here that the check does
// not refuse the real service: an elevated process creates the pipe owned
// by Administrators, exactly as `svpn-service run` does. It runs a real
// status call through the real pipe so that Listen, the DACL and Dial are
// shown to work together, not just to fail apart.
func Test_a_pipe_created_elevated_is_trusted_and_carries_the_protocol(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("not elevated: a pipe created here is owned by this user and is rightly refused")
	}
	listener, err := Listen()
	if err != nil {
		t.Skipf("the pipe name is taken, probably by a running service: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- Serve(ctx, listener, new_fake_service()) }()

	conn, err := Dial(ctx)
	if err != nil {
		t.Fatalf("Dial() to a pipe created elevated = %v, want a connection", err)
	}
	client := New_client(conn)
	if _, err := client.Status(ctx); err != nil {
		t.Fatalf("Status() over the real pipe = %v", err)
	}
	client.Close()

	cancel()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after its context was cancelled")
	}
}

// Test_a_missing_pipe_says_the_service_is_not_running guards the other
// half: an absent service must not be reported as an impersonator, or the
// most common failure there is would read as an attack.
func Test_a_missing_pipe_says_the_service_is_not_running(t *testing.T) {
	listener, err := Listen()
	if err != nil {
		t.Skipf("the pipe name is taken, probably by a running service: %v", err)
	}
	listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx)
	if err == nil {
		conn.Close()
		t.Fatal("Dial() succeeded with no pipe to connect to")
	}
	if !errors.Is(err, Err_service_not_running) {
		t.Fatalf("Dial() with no pipe = %v, want Err_service_not_running", err)
	}
}
