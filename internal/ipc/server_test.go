package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"singularity-vpn-app/internal/events"
)

// --- fakes ---------------------------------------------------------------

// fake_service is the Service a test hands to Serve. Every method is
// scriptable and records what it was called with, the same shape
// fake_control_plane in internal/connection uses for the same reason: a
// test asserts exactly what ran and with what, not just that something did.
type fake_service struct {
	status_result Status_result
	status_err    error

	strategy         Strategy
	set_strategy_err error
	servers          Servers_result
	servers_err      error

	// calls_mu guards connect_calls. Connect runs on the server's dispatch
	// goroutine while the test goroutine watches for it to arrive, so
	// reading the slice without a lock is a data race -- one the toolchain
	// here cannot catch, because go test -race does not run under Cygwin.
	// connect_entered is how a test waits for that arrival instead of
	// spinning on the slice's length.
	calls_mu        sync.Mutex
	connect_calls   []Connect_params
	connect_entered chan struct{}
	entered_once    sync.Once

	connect_err   error
	connect_block chan struct{} // if non-nil, Connect waits on it before returning

	disconnect_calls int
	disconnect_err   error

	sign_in_calls  []Sign_in_params
	sign_in_result Sign_in_result
	sign_in_err    error

	sign_out_calls int
	sign_out_err   error

	version_result Version_result
	version_err    error

	bus *events.Bus
}

func (f *fake_service) Status(ctx context.Context) (Status_result, error) {
	return f.status_result, f.status_err
}

func (f *fake_service) Connect(ctx context.Context, params Connect_params) error {
	f.calls_mu.Lock()
	f.connect_calls = append(f.connect_calls, params)
	f.calls_mu.Unlock()
	f.entered_once.Do(func() { close(f.connect_entered) })
	if f.connect_block != nil {
		<-f.connect_block
	}
	return f.connect_err
}

// connects returns a copy of what Connect has been called with, taken
// under the lock so a test may look while the server is still running.
func (f *fake_service) connects() []Connect_params {
	f.calls_mu.Lock()
	defer f.calls_mu.Unlock()
	return append([]Connect_params(nil), f.connect_calls...)
}

// await_connect blocks until Connect has reached the fake, or fails the
// test. Every place that needs this is testing something that only means
// anything while a connect is genuinely outstanding.
func (f *fake_service) await_connect(t *testing.T) {
	t.Helper()
	select {
	case <-f.connect_entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Connect never reached the fake service")
	}
}

func (f *fake_service) Disconnect(ctx context.Context) error {
	f.disconnect_calls++
	return f.disconnect_err
}

func (f *fake_service) Sign_in(ctx context.Context, params Sign_in_params) (Sign_in_result, error) {
	f.sign_in_calls = append(f.sign_in_calls, params)
	return f.sign_in_result, f.sign_in_err
}

func (f *fake_service) Sign_out(ctx context.Context) error {
	f.sign_out_calls++
	return f.sign_out_err
}

func (f *fake_service) Subscribe() (<-chan events.Event, func()) {
	return f.bus.Subscribe()
}

func (f *fake_service) Version(ctx context.Context) (Version_result, error) {
	return f.version_result, f.version_err
}

func (f *fake_service) Strategy(ctx context.Context) (Strategy, error) {
	return f.strategy, nil
}

func (f *fake_service) Set_strategy(ctx context.Context, strategy Strategy) error {
	f.strategy = strategy
	return f.set_strategy_err
}

func (f *fake_service) Servers(ctx context.Context) (Servers_result, error) {
	return f.servers, f.servers_err
}

// new_fake_service returns a fake_service with its bus ready, so a test
// that never touches Subscribe still has one to close.
func new_fake_service() *fake_service {
	return &fake_service{bus: events.New_bus(8), connect_entered: make(chan struct{})}
}

// serve_pair starts Serve over one half of a net.Pipe() and returns a
// Client wired to the other half, plus a cancel that ends Serve and a
// function a test defers to wait for Serve to actually return.
func serve_pair(t *testing.T, service Service) (*Client, context.CancelFunc) {
	t.Helper()

	server_conn, client_conn := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	listener := &single_conn_listener{conn: server_conn, closed: make(chan struct{})}
	go func() {
		defer close(done)
		Serve(ctx, listener, service)
	}()

	client := New_client(client_conn)

	t.Cleanup(func() {
		cancel()
		client.Close()
		<-done
	})

	return client, cancel
}

// single_conn_listener turns one already-connected net.Conn into a
// net.Listener that hands it out exactly once, then blocks until closed.
// net.Pipe() has no listener of its own, and these tests need Serve driving
// one side of a pipe conn without a real network -- stage 4B is what wires
// Serve to an actual named-pipe listener.
type single_conn_listener struct {
	conn   net.Conn
	once   sync.Once
	closed chan struct{}
}

func (l *single_conn_listener) Accept() (net.Conn, error) {
	first := false
	l.once.Do(func() { first = true })
	if first {
		return l.conn, nil
	}
	<-l.closed
	return nil, errors.New("ipc: test listener closed")
}

// Close unblocks Accept and deliberately does not touch the connection it
// already handed out. A real net.Listener does not close accepted
// connections either, and closing it here would hide the whole shutdown
// question: Serve waits for its connections, so something other than the
// listener has to reach a connection sitting in Scan. Closing the conn from
// here made that look as if it already worked.
func (l *single_conn_listener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (l *single_conn_listener) Addr() net.Addr {
	return l.conn.LocalAddr()
}

func with_timeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// --- tests -----------------------------------------------------------------

func Test_a_status_request_gets_its_own_result(t *testing.T) {
	service := new_fake_service()
	service.status_result = Status_result{State: "connected", Signed_in: true, Node_id: "node-1"}

	client, _ := serve_pair(t, service)

	got, err := client.Status(with_timeout(t))
	if err != nil {
		t.Fatalf("Status() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got, service.status_result) {
		t.Fatalf("Status() = %+v, want %+v", got, service.status_result)
	}
}

func Test_an_unknown_command_is_refused_by_name(t *testing.T) {
	service := new_fake_service()
	client, _ := serve_pair(t, service)

	resp, err := client.call(with_timeout(t), "rm_rf", nil)
	if err != nil {
		t.Fatalf("call(rm_rf) transport error = %v, want a response", err)
	}
	if resp.Ok {
		t.Fatalf("call(rm_rf) Ok = true, want false")
	}
	if resp.Error == nil || resp.Error.Code != Error_unknown_command {
		t.Fatalf("call(rm_rf) error = %+v, want code %q", resp.Error, Error_unknown_command)
	}
	if !strings.Contains(resp.Error.Message, "rm_rf") {
		t.Fatalf("call(rm_rf) error message = %q, want it to name the command", resp.Error.Message)
	}
}

func Test_a_malformed_frame_does_not_close_the_connection(t *testing.T) {
	service := new_fake_service()
	service.status_result = Status_result{State: "disconnected"}
	client, _ := serve_pair(t, service)

	// A deliberately distinct id, so it cannot collide with the one
	// Client.Status assigns its own request below.
	if err := client.write_request(Request{Id: "malformed-frame", Command: ""}); err != nil {
		t.Fatalf("writing a malformed request: %v", err)
	}

	// The malformed frame carried no command and no id the client
	// registered a waiter for, so its response is unclaimed and dropped by
	// design (see Client.deliver) -- what matters here is that the
	// connection survives to answer the next, well-formed request.
	got, err := client.Status(with_timeout(t))
	if err != nil {
		t.Fatalf("Status() after a malformed frame: error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got, service.status_result) {
		t.Fatalf("Status() after a malformed frame = %+v, want %+v", got, service.status_result)
	}
}

func Test_an_oversized_frame_closes_the_connection(t *testing.T) {
	service := new_fake_service()
	client, _ := serve_pair(t, service)

	// Built by hand rather than through json.Marshal(Request{...}), which
	// validates that Params is well-formed JSON on its own and would
	// refuse an arbitrary run of bytes long before this line ever reaches
	// the wire.
	oversized := []byte(`{"id":"1","command":"status","params":"` +
		strings.Repeat("a", Max_frame_bytes+1) + `"}`)
	_ = client.write_request_raw(oversized)

	if _, err := client.Status(with_timeout(t)); err == nil {
		t.Fatal("Status() after an oversized frame = nil error, want the connection to have closed")
	}
}

func Test_a_status_query_is_answered_while_a_connect_is_in_flight(t *testing.T) {
	service := new_fake_service()
	service.connect_block = make(chan struct{})
	service.status_result = Status_result{State: "connecting"}

	client, _ := serve_pair(t, service)

	connect_done := make(chan error, 1)
	go func() {
		connect_done <- client.Connect(with_timeout(t), Connect_params{Mode: Mode_auto})
	}()

	// Give Connect a moment to actually reach the fake and block there,
	// so this test would deadlock -- rather than pass by accident -- if
	// requests were not dispatched concurrently.
	service.await_connect(t)

	// While it is in flight, assert the parameters survived the wire --
	// nothing else in this package checks that a decoded Connect_params is
	// the one the caller sent.
	if got := service.connects(); len(got) != 1 || got[0] != (Connect_params{Mode: Mode_auto}) {
		t.Fatalf("the service saw Connect%+v, want exactly one call with %+v", got, Connect_params{Mode: Mode_auto})
	}

	status, err := client.Status(with_timeout(t))
	if err != nil {
		t.Fatalf("Status() while connect is in flight: error = %v, want nil", err)
	}
	if !reflect.DeepEqual(status, service.status_result) {
		t.Fatalf("Status() while connect is in flight = %+v, want %+v", status, service.status_result)
	}

	close(service.connect_block)
	if err := <-connect_done; err != nil {
		t.Fatalf("Connect() = %v, want nil", err)
	}
}

func Test_a_service_error_becomes_its_code(t *testing.T) {
	other := errors.New("boom")

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"not_signed_in", fmt.Errorf("wrapped: %w", Err_not_signed_in), Error_not_signed_in},
		{"unauthorized", fmt.Errorf("wrapped: %w", Err_unauthorized), Error_unauthorized},
		{"busy", fmt.Errorf("wrapped: %w", Err_busy), Error_busy},
		{"unrecognised", other, Error_internal},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			service := new_fake_service()
			service.disconnect_err = c.err
			client, _ := serve_pair(t, service)

			err := client.Disconnect(with_timeout(t))
			var remote *Remote_error
			if !errors.As(err, &remote) {
				t.Fatalf("Disconnect() error = %v (%T), want a *Remote_error", err, err)
			}
			if remote.Code != c.want {
				t.Fatalf("Disconnect() code = %q, want %q", remote.Code, c.want)
			}
			if remote.Message != c.err.Error() {
				t.Fatalf("Disconnect() message = %q, want the error's own text %q", remote.Message, c.err.Error())
			}
		})
	}
}

// Test_cancelling_serve_closes_a_live_connection is the shutdown path, and
// it deliberately never closes the client. A connection that is sitting in
// Scan is not reached by closing the listener: only the accepted conn's own
// Close unblocks it, and Serve waits for every handler before returning.
// Without that, the service's stop -- the code that takes the routes and
// the DNS back off the machine -- waits behind one idle interface until the
// service control manager gives up and kills the process with the tunnel
// still up.
func Test_cancelling_serve_closes_a_live_connection(t *testing.T) {
	service := new_fake_service()
	server_conn, client_conn := net.Pipe()
	defer client_conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	listener := &single_conn_listener{conn: server_conn, closed: make(chan struct{})}

	returned := make(chan error, 1)
	go func() { returned <- Serve(ctx, listener, service) }()

	client := New_client(client_conn)
	if _, err := client.Status(context.Background()); err != nil {
		t.Fatalf("Status() = %v, want the connection to be serving before the test cancels it", err)
	}

	cancel()

	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve() = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not return within 2s of its context being cancelled, with one connection still open and never closed by the client")
	}
}

// Test_a_subscribe_that_races_the_shutdown_still_lets_serve_return covers
// the window between the read loop deciding to unsubscribe and a subscribe
// handler storing its cancel function. A subscription stored after that
// point is one nobody will ever cancel, and the event pump would hold the
// connection's handler group open on a channel that never closes.
func Test_a_subscribe_that_races_the_shutdown_still_lets_serve_return(t *testing.T) {
	service := new_fake_service()
	conn, _ := net.Pipe()
	h := &conn_handler{conn: conn, service: service, sem: make(chan struct{}, max_in_flight)}

	// Stand where serve_conn stands after its read loop has ended.
	h.sub_mu.Lock()
	h.closing = true
	h.sub_mu.Unlock()

	var handlers sync.WaitGroup
	go h.handle_subscribe(Request{Id: "1", Command: Command_subscribe}, &handlers)

	done := make(chan struct{})
	go func() { handlers.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the connection's handlers never finished: a subscription stored after the read loop unsubscribed is never cancelled, so the event pump never ends")
	}
}
