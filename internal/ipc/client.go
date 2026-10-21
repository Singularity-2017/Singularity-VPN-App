package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"
)

// client_event_buffer mirrors events.default_buffer: enough that a burst of
// state changes does not have to be read back faster than it was produced,
// without buffering an unbounded backlog a stalled reader never drains.
const client_event_buffer = 64

// Remote_error is a Response with Ok: false, translated into a Go error a
// caller can act on.
//
// It carries an Is method rather than a set of plain sentinel values: a
// caller compares with errors.Is(err, ipc.Remote_not_signed_in) the same
// way it already does for every other sentinel in this codebase, and Code
// stays a plain string so a code this package has not yet grown a sentinel
// for is still visible on the struct rather than silently uncomparable.
type Remote_error struct {
	Code    string
	Message string
}

func (e *Remote_error) Error() string {
	return fmt.Sprintf("ipc: %s: %s", e.Code, e.Message)
}

// Is reports whether target names the same error code. Message is never
// compared: two Remote_errors with the same Code are the same kind of
// refusal even if the service worded them differently.
func (e *Remote_error) Is(target error) bool {
	other, ok := target.(*Remote_error)
	if !ok || other.Code == "" {
		return false
	}
	return other.Code == e.Code
}

// The sentinels a caller matches Remote_error against with errors.Is, one
// per code in protocol.go that a caller needs to branch on. Stage 5's
// interface has to tell Remote_not_signed_in from Remote_unauthorized to
// know whether to show a sign-in form or a "wrong password" message.
var (
	Remote_not_signed_in = &Remote_error{Code: Error_not_signed_in}
	Remote_unauthorized  = &Remote_error{Code: Error_unauthorized}
	Remote_busy          = &Remote_error{Code: Error_busy}

	Remote_tunnel_up = &Remote_error{Code: Error_tunnel_up}

	Remote_server_unavailable   = &Remote_error{Code: Error_server_unavailable}
	Remote_no_server_in_country = &Remote_error{Code: Error_no_server_in_country}
)

// Client speaks the protocol over one connection. It is safe for
// concurrent use: requests are correlated by id, so a status query issued
// while a connect is in flight gets the right answer rather than the
// connect's.
//
// Client does not dial. It takes a net.Conn because dialling the named pipe
// is Windows-specific and belongs to a later stage, and because that is
// what lets every test in this package run over net.Pipe().
type Client struct {
	conn net.Conn

	write_mu sync.Mutex

	mu        sync.Mutex
	pending   map[string]chan Response
	next_id   int
	closed    bool
	close_err error

	done   chan struct{}
	events chan Event
}

// New_client starts one read goroutine over conn and returns immediately.
func New_client(conn net.Conn) *Client {
	c := &Client{
		conn:    conn,
		pending: make(map[string]chan Response),
		done:    make(chan struct{}),
		events:  make(chan Event, client_event_buffer),
	}
	go c.read_loop()
	return c
}

// Close closes the underlying connection. The read goroutine notices and
// unblocks every call still waiting.
func (c *Client) Close() error {
	return c.conn.Close()
}

func (c *Client) Status(ctx context.Context) (Status_result, error) {
	var result Status_result
	resp, err := c.call(ctx, Command_status, nil)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}

func (c *Client) Connect(ctx context.Context, params Connect_params) error {
	resp, err := c.call(ctx, Command_connect, params)
	if err != nil {
		return err
	}
	return decode_result(resp, nil)
}

func (c *Client) Disconnect(ctx context.Context) error {
	resp, err := c.call(ctx, Command_disconnect, nil)
	if err != nil {
		return err
	}
	return decode_result(resp, nil)
}

func (c *Client) Sign_in(ctx context.Context, params Sign_in_params) (Sign_in_result, error) {
	var result Sign_in_result
	resp, err := c.call(ctx, Command_sign_in, params)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}

func (c *Client) Sign_out(ctx context.Context) error {
	resp, err := c.call(ctx, Command_sign_out, nil)
	if err != nil {
		return err
	}
	return decode_result(resp, nil)
}

func (c *Client) Version(ctx context.Context) (Version_result, error) {
	var result Version_result
	resp, err := c.call(ctx, Command_version, nil)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}

func (c *Client) Strategy(ctx context.Context) (Strategy, error) {
	var result Strategy
	resp, err := c.call(ctx, Command_strategy, nil)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}

func (c *Client) Set_strategy(ctx context.Context, strategy Strategy) error {
	resp, err := c.call(ctx, Command_set_strategy, strategy)
	if err != nil {
		return err
	}
	return decode_result(resp, nil)
}

func (c *Client) Games(ctx context.Context) (Games, error) {
	var result Games
	resp, err := c.call(ctx, Command_games, nil)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}

func (c *Client) Set_games(ctx context.Context, games Games) error {
	resp, err := c.call(ctx, Command_set_games, games)
	if err != nil {
		return err
	}
	return decode_result(resp, nil)
}

func (c *Client) Answer_match_server(ctx context.Context, answer Match_server_answer) error {
	resp, err := c.call(ctx, Command_answer_match_server, answer)
	if err != nil {
		return err
	}
	return decode_result(resp, nil)
}

func (c *Client) Journal(ctx context.Context) (Journal_result, error) {
	var result Journal_result
	resp, err := c.call(ctx, Command_journal, nil)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}

func (c *Client) Tunnel_mode(ctx context.Context) (Tunnel_mode, error) {
	var result Tunnel_mode
	resp, err := c.call(ctx, Command_tunnel_mode, nil)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}

func (c *Client) Profiles(ctx context.Context) (Profiles_result, error) {
	var result Profiles_result
	resp, err := c.call(ctx, Command_profiles, nil)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}

func (c *Client) Set_profile(ctx context.Context, profile Set_profile) error {
	resp, err := c.call(ctx, Command_set_profile, profile)
	if err != nil {
		return err
	}
	return decode_result(resp, nil)
}

func (c *Client) Set_tunnel_mode(ctx context.Context, mode Tunnel_mode) error {
	resp, err := c.call(ctx, Command_set_tunnel_mode, mode)
	if err != nil {
		return err
	}
	return decode_result(resp, nil)
}

func (c *Client) Servers(ctx context.Context) (Servers_result, error) {
	var result Servers_result
	resp, err := c.call(ctx, Command_servers, nil)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}

func (c *Client) Ping_servers(ctx context.Context) (Ping_servers_result, error) {
	var result Ping_servers_result
	resp, err := c.call(ctx, Command_ping_servers, nil)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}

func (c *Client) Analyze(ctx context.Context) (Analysis_result, error) {
	var result Analysis_result
	resp, err := c.call(ctx, Command_analyze, nil)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}

func (c *Client) Analysis(ctx context.Context) (Last_analysis, error) {
	var result Last_analysis
	resp, err := c.call(ctx, Command_analysis, nil)
	if err != nil {
		return result, err
	}
	return result, decode_result(resp, &result)
}

// Subscribe asks the service for the event stream and returns a channel
// that carries it until the connection closes or ctx ends waiting for the
// acknowledgement.
func (c *Client) Subscribe(ctx context.Context) (<-chan Event, error) {
	resp, err := c.call(ctx, Command_subscribe, nil)
	if err != nil {
		return nil, err
	}
	if err := decode_result(resp, nil); err != nil {
		return nil, err
	}
	return c.events, nil
}

// call sends one request and waits for its response, its ctx to end, or
// the connection to die, whichever comes first.
func (c *Client) call(ctx context.Context, command string, params any) (Response, error) {
	var raw json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return Response{}, fmt.Errorf("ipc: encoding %s params: %w", command, err)
		}
		raw = data
	}

	c.mu.Lock()
	if c.closed {
		err := c.close_err
		c.mu.Unlock()
		return Response{}, err
	}
	c.next_id++
	id := strconv.Itoa(c.next_id)
	wait := make(chan Response, 1)
	c.pending[id] = wait
	c.mu.Unlock()

	if err := c.write_request(Request{Id: id, Command: command, Params: raw}); err != nil {
		c.unregister(id)
		return Response{}, err
	}

	select {
	case resp, ok := <-wait:
		if !ok {
			return Response{}, c.closed_error()
		}
		return resp, nil
	case <-c.done:
		c.unregister(id)
		return Response{}, c.closed_error()
	case <-ctx.Done():
		c.unregister(id)
		return Response{}, ctx.Err()
	}
}

func (c *Client) unregister(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) closed_error() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.close_err
}

func (c *Client) write_request(req Request) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("ipc: encoding request: %w", err)
	}
	return c.write_request_raw(data)
}

// write_request_raw writes one already-encoded frame, without its trailing
// newline, onto the wire. It exists as its own method, rather than being
// folded into write_request, so a test can drive the server with a frame
// json.Marshal(Request{...}) would never itself produce -- for instance one
// larger than Max_frame_bytes.
func (c *Client) write_request_raw(data []byte) error {
	data = append(data, '\n')

	c.write_mu.Lock()
	defer c.write_mu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(Write_timeout)); err != nil {
		return err
	}
	_, err := c.conn.Write(data)
	return err
}

// read_loop decodes each line, reads Type, and either hands a Response to
// the waiter registered under its Id or pushes an Event onto the
// subscription channel. It runs until the connection dies, then unblocks
// every pending call and closes the event channel.
func (c *Client) read_loop() {
	scanner := bufio.NewScanner(c.conn)
	scanner.Buffer(make([]byte, 0, 4096), Max_frame_bytes)

	for scanner.Scan() {
		line := scanner.Bytes()

		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			// The server only ever writes well-formed frames; a torn line
			// here means the connection is on its way down, and the loop
			// below (scanner.Err or a clean EOF) is what notices that.
			continue
		}

		switch probe.Type {
		case Type_event:
			var ev Event
			if err := json.Unmarshal(line, &ev); err != nil {
				continue
			}
			select {
			case c.events <- ev:
			default:
				// A subscriber that has fallen behind must not be able to
				// wedge this loop, and with it, every call still waiting
				// on its own response -- the same contract events.Bus
				// makes with a slow listener.
			}
		default:
			var resp Response
			if err := json.Unmarshal(line, &resp); err != nil {
				continue
			}
			c.deliver(resp)
		}
	}

	cause := scanner.Err()
	if cause == nil {
		cause = errors.New("connection closed")
	}
	c.shut_down(fmt.Errorf("ipc: %w", cause))
}

// deliver hands resp to the call waiting on its Id. A response for an id
// nobody is waiting for is dropped, not an error -- that is what a call
// whose ctx ended first leaves behind.
func (c *Client) deliver(resp Response) {
	c.mu.Lock()
	wait, ok := c.pending[resp.Id]
	if ok {
		delete(c.pending, resp.Id)
	}
	c.mu.Unlock()
	if !ok {
		return
	}
	wait <- resp
}

// shut_down runs once, when read_loop ends: every waiting call is released
// with err, and the event channel closes, so a call made after this point
// gets the same err back rather than blocking forever.
func (c *Client) shut_down(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.close_err = err
	pending := c.pending
	c.pending = nil
	c.mu.Unlock()

	for _, wait := range pending {
		close(wait)
	}
	close(c.done)
	close(c.events)
}

// decode_result turns a Response into (decoded value, error): a Remote_error
// built from Error_body when Ok is false, or out populated from Result
// otherwise. out may be nil for a call with nothing to decode.
func decode_result(resp Response, out any) error {
	if !resp.Ok {
		if resp.Error == nil {
			return &Remote_error{Code: Error_internal, Message: "server refused the request without an error body"}
		}
		return &Remote_error{Code: resp.Error.Code, Message: resp.Error.Message}
	}
	if out == nil || len(resp.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return fmt.Errorf("ipc: decoding result: %w", err)
	}
	return nil
}
