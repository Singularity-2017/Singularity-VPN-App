package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"singularity-vpn-app/internal/events"
)

// max_in_flight bounds how many requests one connection may have running at
// once. This is not an optimisation: connection.Config.Connect_timeout gives
// Connect up to 30 seconds, and an interface that cannot ask for status
// while a connect is running has nothing to show the person who is
// waiting. When the bound is reached, a new request is answered Error_busy
// immediately rather than queued behind whatever is already running.
const max_in_flight = 8

// Serve accepts connections on listener until ctx ends or Accept fails for
// a reason of its own, handling each connection in its own goroutine, and
// waits for all of them to finish before returning.
//
// When ctx ends, the listener is closed -- that is what makes a blocked
// Accept return -- and Serve returns ctx.Err(). An Accept error that
// follows is not a failure being reported twice: it is the close doing its
// job.
func Serve(ctx context.Context, listener net.Listener, service Service) error {
	var conns sync.WaitGroup
	defer conns.Wait()

	stop_watching := make(chan struct{})
	defer close(stop_watching)
	go func() {
		select {
		case <-ctx.Done():
			listener.Close()
		case <-stop_watching:
		}
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}

		conns.Add(1)
		go func() {
			defer conns.Done()
			serve_conn(ctx, conn, service)
		}()
	}
}

// conn_handler is the state one connection needs beyond the read loop
// itself: a mutex serialising every write onto the wire, a semaphore
// bounding concurrent handlers, and whatever subscription this connection
// has asked for.
type conn_handler struct {
	conn    net.Conn
	service Service
	sem     chan struct{}

	write_mu sync.Mutex

	sub_mu     sync.Mutex
	subscribed bool
	sub_cancel func()
	closing    bool
}

// serve_conn is the per-connection read loop. Requests are dispatched to
// their own goroutines, bounded by conn_handler.sem, so that a slow
// connect does not stop this connection from answering a status query.
func serve_conn(ctx context.Context, conn net.Conn, service Service) {
	defer conn.Close()

	// A cancelled context has to reach a connection that is sitting in
	// Scan, not only the listener. Closing the listener stops new
	// connections arriving and does nothing whatever to the ones already
	// accepted -- and Serve waits for all of them before it returns. Left
	// out, a single idle interface still holding the pipe open keeps the
	// whole service alive through its own stop: the service control
	// manager waits, loses patience, and kills the process with a tunnel
	// up and the machine's routing table still rewritten. The teardown
	// that matters is the one that runs after Serve returns.
	conn_done := make(chan struct{})
	defer close(conn_done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-conn_done:
		}
	}()

	h := &conn_handler{
		conn:    conn,
		service: service,
		sem:     make(chan struct{}, max_in_flight),
	}

	var handlers sync.WaitGroup

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 4096), Max_frame_bytes)

	for scanner.Scan() {
		// Scanner.Bytes() is only valid until the next Scan(); the
		// dispatch goroutine reads it after this loop has moved on, so it
		// needs its own copy.
		line := append([]byte(nil), scanner.Bytes()...)

		var req Request
		if err := json.Unmarshal(line, &req); err != nil || req.Command == "" {
			// A single malformed frame is a caller's bug, not grounds for
			// dropping a connection that may have a tunnel riding on it.
			h.write_response(Response{Type: Type_response, Id: req.Id, Ok: false,
				Error: &Error_body{Code: Error_bad_request, Message: "malformed request or missing command"}})
			continue
		}

		select {
		case h.sem <- struct{}{}:
		default:
			h.write_response(Response{Type: Type_response, Id: req.Id, Ok: false,
				Error: &Error_body{Code: Error_busy, Message: "too many requests already in flight on this connection"}})
			continue
		}

		handlers.Add(1)
		go func(req Request) {
			defer handlers.Done()
			defer func() { <-h.sem }()
			h.dispatch(ctx, req, &handlers)
		}(req)
	}

	if err := scanner.Err(); err != nil {
		// The scanner only ever fails here for bufio.ErrTooLong -- a line
		// past Max_frame_bytes -- or a read error on the connection itself.
		// Either way there is no reliable place to resynchronise on the
		// next line, so this is the last frame this connection gets, sent
		// with an empty Id because whatever line caused this was never
		// successfully parsed.
		h.write_response(Response{Type: Type_response, Ok: false,
			Error: &Error_body{Code: Error_bad_request, Message: fmt.Sprintf("frame too large or unreadable: %v", err)}})
	}

	h.sub_mu.Lock()
	h.closing = true
	cancel := h.sub_cancel
	h.sub_mu.Unlock()
	if cancel != nil {
		// Unsubscribing closes the channel Subscribe returned, which is
		// what lets the event pump goroutine below return on its own.
		cancel()
	}

	handlers.Wait()
}

// dispatch is a switch over the command constants with a default that
// answers Error_unknown_command naming the command it did not recognise.
// There is no map from name to handler: a switch is what makes the closed
// set visible to anyone reading this file, and docs/WINDOWS_CLIENT.md
// section 45 is about exactly that.
//
// The context handed to a handler is the connection's context, unmodified.
// Connect already carries its own timeout inside the state machine; a
// second, shorter one added here would cut a legitimate connect in half.
//
// Never log a request's params here or anywhere else in this package --
// there is no logger in it at all, and Serve takes none. That keeps
// Sign_in_params out of every log file by construction rather than by
// remembering.
func (h *conn_handler) dispatch(ctx context.Context, req Request, handlers *sync.WaitGroup) {
	switch req.Command {
	case Command_status:
		result, err := h.service.Status(ctx)
		h.reply(req.Id, result, err)

	case Command_connect:
		params, err := decode_params[Connect_params](req)
		if err != nil {
			h.respond_bad_request(req.Id, err)
			return
		}
		if err := params.Validate(); err != nil {
			h.respond_bad_request(req.Id, err)
			return
		}
		h.reply(req.Id, nil, h.service.Connect(ctx, params))

	case Command_disconnect:
		h.reply(req.Id, nil, h.service.Disconnect(ctx))

	case Command_sign_in:
		params, err := decode_params[Sign_in_params](req)
		if err != nil {
			h.respond_bad_request(req.Id, err)
			return
		}
		if err := params.Validate(); err != nil {
			h.respond_bad_request(req.Id, err)
			return
		}
		result, err := h.service.Sign_in(ctx, params)
		h.reply(req.Id, result, err)

	case Command_sign_out:
		h.reply(req.Id, nil, h.service.Sign_out(ctx))

	case Command_subscribe:
		h.handle_subscribe(req, handlers)

	case Command_version:
		result, err := h.service.Version(ctx)
		h.reply(req.Id, result, err)

	case Command_strategy:
		result, err := h.service.Strategy(ctx)
		h.reply(req.Id, result, err)

	case Command_set_strategy:
		params, err := decode_params[Strategy](req)
		if err != nil {
			h.respond_bad_request(req.Id, err)
			return
		}
		if err := params.Validate(); err != nil {
			h.respond_bad_request(req.Id, err)
			return
		}
		h.reply(req.Id, nil, h.service.Set_strategy(ctx, params))

	case Command_games:
		result, err := h.service.Games(ctx)
		h.reply(req.Id, result, err)

	case Command_set_games:
		params, err := decode_params[Games](req)
		if err != nil {
			h.respond_bad_request(req.Id, err)
			return
		}
		if err := params.Validate(); err != nil {
			h.respond_bad_request(req.Id, err)
			return
		}
		h.reply(req.Id, nil, h.service.Set_games(ctx, params))

	case Command_tunnel_mode:
		result, err := h.service.Tunnel_mode(ctx)
		h.reply(req.Id, result, err)

	case Command_journal:
		result, err := h.service.Journal(ctx)
		h.reply(req.Id, result, err)

	case Command_set_tunnel_mode:
		params, err := decode_params[Tunnel_mode](req)
		if err != nil {
			h.respond_bad_request(req.Id, err)
			return
		}
		if err := params.Validate(); err != nil {
			h.respond_bad_request(req.Id, err)
			return
		}
		h.reply(req.Id, nil, h.service.Set_tunnel_mode(ctx, params))

	case Command_servers:
		result, err := h.service.Servers(ctx)
		h.reply(req.Id, result, err)

	case Command_ping_servers:
		result, err := h.service.Ping_servers(ctx)
		h.reply(req.Id, result, err)

	case Command_analyze:
		result, err := h.service.Analyze(ctx)
		h.reply(req.Id, result, err)

	case Command_analysis:
		result, err := h.service.Analysis(ctx)
		h.reply(req.Id, result, err)

	default:
		h.write_response(Response{Type: Type_response, Id: req.Id, Ok: false,
			Error: &Error_body{Code: Error_unknown_command, Message: fmt.Sprintf("unknown command %q", req.Command)}})
	}
}

// decode_params reads Request.Params into T. Params missing entirely
// decodes to T's zero value rather than an error -- Validate is what tells
// a caller that forgot a field apart from one that sent it empty on
// purpose, and it does that uniformly whether the field was absent or "".
func decode_params[T any](req Request) (T, error) {
	var params T
	if len(req.Params) == 0 {
		return params, nil
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return params, fmt.Errorf("ipc: decoding params for %s: %w", req.Command, err)
	}
	return params, nil
}

// handle_subscribe is the one command that changes the connection rather
// than doing something: it answers ok with an empty result, and from then
// on the write loop below delivers Event frames as service.Subscribe's
// channel produces them. A second subscribe on the same connection is
// answered ok and changes nothing -- idempotent, because a client that
// reconnects its own event pump should not have to track whether it
// already asked.
func (h *conn_handler) handle_subscribe(req Request, handlers *sync.WaitGroup) {
	h.sub_mu.Lock()
	if h.subscribed {
		h.sub_mu.Unlock()
		h.respond_ok(req.Id, nil)
		return
	}
	h.subscribed = true
	channel, cancel := h.service.Subscribe()
	h.sub_cancel = cancel
	closing := h.closing
	h.sub_mu.Unlock()

	if closing {
		// The read loop has already gone past the point where it
		// unsubscribes, so nothing else will ever cancel this one, and
		// pump_events would hold handlers.Wait open on a channel nobody
		// closes -- serve_conn would never return and neither would
		// Serve. Cancelling here closes the channel, so the pump below
		// finds it closed and exits at once.
		cancel()
	}

	handlers.Add(1)
	go func() {
		defer handlers.Done()
		h.pump_events(channel)
	}()

	h.respond_ok(req.Id, nil)
}

// pump_events turns each events.Event into an Event frame and writes it.
// It returns when channel is closed, which is what unsubscribing at the end
// of serve_conn causes.
func (h *conn_handler) pump_events(channel <-chan events.Event) {
	for ev := range channel {
		var data json.RawMessage
		if ev.Data != nil {
			marshalled, err := json.Marshal(ev.Data)
			if err == nil {
				data = marshalled
			}
			// If Data will not marshal, the event still goes out with Data
			// omitted. The kind and the time are the part a person needs;
			// dropping the whole event because one payload could not be
			// encoded would hide that anything happened at all.
		}
		h.write_event(Event{Type: Type_event, Kind: string(ev.Kind), At: ev.At, Session_id: ev.Session_id, Data: data})
	}
}

// reply answers a request that carries a Service call's (result, error):
// Error_body mapped from err when it is not nil, an ok response carrying
// result otherwise. result may be nil for a command with nothing to return.
func (h *conn_handler) reply(id string, result any, err error) {
	if err != nil {
		h.respond_err(id, err)
		return
	}
	h.respond_ok(id, result)
}

// respond_ok marshals result, if any, into Response.Result. A nil result
// leaves it empty, which -- because Result is json.RawMessage with
// omitempty -- means the field is absent from the JSON rather than "null"
// or "{}".
func (h *conn_handler) respond_ok(id string, result any) {
	var raw json.RawMessage
	if result != nil {
		if marshalled, err := json.Marshal(result); err == nil {
			raw = marshalled
		}
		// Marshalling one of this package's own result types cannot fail
		// in practice; if it somehow did, an empty result is a smaller
		// lie than tearing down the connection over it.
	}
	h.write_response(Response{Type: Type_response, Id: id, Ok: true, Result: raw})
}

// respond_err maps err to the Error_body the sentinels in protocol.go
// describe.
func (h *conn_handler) respond_err(id string, err error) {
	h.write_response(Response{Type: Type_response, Id: id, Ok: false, Error: error_body_for(err)})
}

// respond_bad_request answers a request whose params could not be decoded
// or did not pass Validate. This is not error_body_for's job: a Validate
// failure is never one of the sentinels in protocol.go, only ever
// Error_bad_request.
func (h *conn_handler) respond_bad_request(id string, err error) {
	h.write_response(Response{Type: Type_response, Id: id, Ok: false,
		Error: &Error_body{Code: Error_bad_request, Message: err.Error()}})
}

// error_body_for maps a Service error to the code a caller is meant to
// branch on, by errors.Is against the sentinels in protocol.go so that a
// Service implementation may wrap them with context. Everything
// unrecognised becomes Error_internal, with the error's own text as the
// message -- see the comment on the sentinels for why that is safe here.
func error_body_for(err error) *Error_body {
	switch {
	case errors.Is(err, Err_not_signed_in):
		return &Error_body{Code: Error_not_signed_in, Message: err.Error()}
	case errors.Is(err, Err_unauthorized):
		return &Error_body{Code: Error_unauthorized, Message: err.Error()}
	case errors.Is(err, Err_busy):
		return &Error_body{Code: Error_busy, Message: err.Error()}
	case errors.Is(err, Err_tunnel_up):
		return &Error_body{Code: Error_tunnel_up, Message: err.Error()}
	case errors.Is(err, Err_server_unavailable):
		return &Error_body{Code: Error_server_unavailable, Message: err.Error()}
	case errors.Is(err, Err_no_server_in_country):
		return &Error_body{Code: Error_no_server_in_country, Message: err.Error()}
	default:
		return &Error_body{Code: Error_internal, Message: err.Error()}
	}
}

// write_response and write_event share one writer: write_line, which takes
// write_mu so that concurrent handlers on the same connection never
// interleave two frames into one garbled line, and sets a write deadline so
// that a client that stopped reading cannot wedge this goroutine behind a
// full pipe buffer.
func (h *conn_handler) write_response(resp Response) {
	if err := h.write_line(resp); err != nil {
		h.conn.Close()
	}
}

func (h *conn_handler) write_event(ev Event) {
	if err := h.write_line(ev); err != nil {
		h.conn.Close()
	}
}

func (h *conn_handler) write_line(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("ipc: encoding frame: %w", err)
	}
	data = append(data, '\n')

	h.write_mu.Lock()
	defer h.write_mu.Unlock()
	if err := h.conn.SetWriteDeadline(time.Now().Add(Write_timeout)); err != nil {
		return err
	}
	_, err = h.conn.Write(data)
	return err
}
