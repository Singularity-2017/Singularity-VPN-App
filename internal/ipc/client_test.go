package ipc

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"singularity-vpn-app/internal/events"
)

func Test_responses_are_matched_to_their_own_requests(t *testing.T) {
	service := new_fake_service()
	client, _ := serve_pair(t, service)

	service.status_result = Status_result{State: "connected", Node_id: "node-a"}
	first, err := client.Status(with_timeout(t))
	if err != nil {
		t.Fatalf("first Status() error = %v, want nil", err)
	}

	service.status_result = Status_result{State: "connecting", Node_id: "node-b"}
	second, err := client.Status(with_timeout(t))
	if err != nil {
		t.Fatalf("second Status() error = %v, want nil", err)
	}

	if first.Node_id != "node-a" {
		t.Fatalf("first call got Node_id = %q, want %q", first.Node_id, "node-a")
	}
	if second.Node_id != "node-b" {
		t.Fatalf("second call got Node_id = %q, want %q", second.Node_id, "node-b")
	}
}

// Test_two_concurrent_calls_each_get_their_own_result exercises the
// correlation that matters most: the fake answers two calls in flight in
// the opposite order they were issued, and each caller must still get the
// result meant for it rather than the other one's.
func Test_two_concurrent_calls_each_get_their_own_result(t *testing.T) {
	service := new_fake_service()
	service.sign_out_err = errors.New("boom")
	release := make(chan struct{})
	service.connect_block = release
	// release must be closed no matter how this test ends, or the fake's
	// Connect (and, with it, Serve's shutdown) hangs forever -- sync.Once
	// because the test also closes it explicitly, further down, once it
	// actually needs Connect to finish.
	var release_once sync.Once
	release_now := func() { release_once.Do(func() { close(release) }) }
	defer release_now()

	client, _ := serve_pair(t, service)

	connect_err := make(chan error, 1)
	go func() {
		connect_err <- client.Connect(with_timeout(t), Connect_params{Mode: Mode_auto})
	}()

	// Wait for Connect to actually be in flight before racing Sign_out
	// past it, the same way
	// Test_a_status_query_is_answered_while_a_connect_is_in_flight does.
	service.await_connect(t)

	// Sign_out itself never blocks in the fake; the point here is that it
	// returns its own scripted error and not Connect's, while Connect is
	// still outstanding.
	err := client.Sign_out(with_timeout(t))
	if err == nil || err.Error() != "ipc: internal: boom" {
		t.Fatalf("Sign_out() while connect in flight = %v, want the sign_out error, not the connect one", err)
	}

	release_now()
	if err := <-connect_err; err != nil {
		// Sign_out's response landing while Connect is still outstanding
		// must not steal or corrupt Connect's own reply -- this is what
		// catches responses being matched by anything other than id.
		t.Fatalf("Connect() = %v, want nil once released", err)
	}
}

func Test_a_subscribed_client_receives_events(t *testing.T) {
	service := new_fake_service()
	client, _ := serve_pair(t, service)

	channel, err := client.Subscribe(with_timeout(t))
	if err != nil {
		t.Fatalf("Subscribe() error = %v, want nil", err)
	}

	at1 := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	at2 := at1.Add(time.Second)
	service.bus.Publish(events.Event{Kind: events.Kind_connection_started, At: at1})
	service.bus.Publish(events.Event{Kind: events.Kind_connection_established, At: at2})

	first := receive_event(t, channel)
	if first.Kind != string(events.Kind_connection_started) || !first.At.Equal(at1) {
		t.Fatalf("first event = %+v, want kind %q at %v", first, events.Kind_connection_started, at1)
	}

	second := receive_event(t, channel)
	if second.Kind != string(events.Kind_connection_established) || !second.At.Equal(at2) {
		t.Fatalf("second event = %+v, want kind %q at %v", second, events.Kind_connection_established, at2)
	}
}

// Test_an_event_whose_data_will_not_marshal_still_arrives is item 8: a
// payload json.Marshal refuses must not swallow the whole event, because
// the kind and the time are what a person needs to know that something
// happened.
func Test_an_event_whose_data_will_not_marshal_still_arrives(t *testing.T) {
	service := new_fake_service()
	client, _ := serve_pair(t, service)

	channel, err := client.Subscribe(with_timeout(t))
	if err != nil {
		t.Fatalf("Subscribe() error = %v, want nil", err)
	}

	at := time.Date(2026, 9, 22, 11, 0, 0, 0, time.UTC)
	service.bus.Publish(events.Event{Kind: events.Kind_error, At: at, Data: make(chan int)})

	got := receive_event(t, channel)
	if got.Kind != string(events.Kind_error) {
		t.Fatalf("got Kind = %q, want %q", got.Kind, events.Kind_error)
	}
	if !got.At.Equal(at) {
		t.Fatalf("got At = %v, want %v", got.At, at)
	}
	if len(got.Data) != 0 {
		t.Fatalf("got Data = %s, want it absent because the payload could not marshal", got.Data)
	}
}

func Test_a_closed_connection_fails_waiting_calls(t *testing.T) {
	service := new_fake_service()
	service.connect_block = make(chan struct{})
	// The fake's Connect handler is left blocked in its own goroutine on
	// the server side even after the pipe itself is closed below; nothing
	// but this release lets Serve's handlers.Wait() ever return.
	defer close(service.connect_block)

	server_conn, client_conn := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	listener := &single_conn_listener{conn: server_conn, closed: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Serve(ctx, listener, service)
	}()
	t.Cleanup(func() { <-done })

	client := New_client(client_conn)

	call_err := make(chan error, 1)
	go func() {
		// No deadline of its own: the only thing that may end this call is
		// the connection dying, which is exactly what this test checks.
		call_err <- client.Connect(context.Background(), Connect_params{Mode: Mode_auto})
	}()

	service.await_connect(t)

	// Close the server side mid-call. The client's read loop must notice
	// and release the call rather than let it hang until its own ctx
	// expires. The connection is closed directly, not through the
	// listener: a listener does not close connections it has already
	// handed out, and relying on a test double that did was hiding the
	// shutdown path this package depends on.
	server_conn.Close()
	listener.Close()

	select {
	case err := <-call_err:
		if err == nil {
			t.Fatal("Connect() after the server closed = nil, want an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Connect() did not return after the server side closed")
	}
}

func receive_event(t *testing.T, channel <-chan Event) Event {
	t.Helper()
	select {
	case ev := <-channel:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event arrived")
		return Event{}
	}
}
