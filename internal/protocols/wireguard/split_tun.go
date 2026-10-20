package wireguard

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/amnezia-vpn/amneziawg-go/tun"
)

// split_tun is the tun device wireguard-go reads and writes when games are
// routed per process. It is the tunnel adapter plus two side doors: packets
// handed to Inject are read by wireguard-go as if the adapter had produced
// them, and packets wireguard-go writes are first offered to the divert
// hook, which takes a game's replies before they reach the adapter.
//
// Game packets never touch the adapter, so the adapter needs no default
// route for them.
type split_tun struct {
	inner tun.Device
	// divert is offered each decrypted packet; true means it took it.
	// It must not keep the slice.
	divert func(packet []byte) bool

	from_inner chan []byte
	injected   chan []byte
	closed     chan struct{}
	close_once sync.Once
	inner_err  error // set before from_inner is closed
}

// err_split_tun_closed wraps os.ErrClosed, which wireguard-go takes as the
// device going away rather than as a failure to log.
var err_split_tun_closed = fmt.Errorf("wireguard: the tunnel device is closed: %w", os.ErrClosed)

// injected_queue bounds packets waiting for wireguard-go. A game that
// outruns the tunnel loses packets here, as it would on a full link,
// rather than growing memory without limit.
const injected_queue = 1024

func new_split_tun(inner tun.Device, divert func(packet []byte) bool) *split_tun {
	t := &split_tun{
		inner:      inner,
		divert:     divert,
		from_inner: make(chan []byte, inner.BatchSize()),
		injected:   make(chan []byte, injected_queue),
		closed:     make(chan struct{}),
	}
	go t.pump()
	return t
}

// pump reads the adapter on its own goroutine, so a packet injected while
// the adapter is idle -- the usual case in games-only mode -- is not stuck
// behind a read that may never return.
func (t *split_tun) pump() {
	defer close(t.from_inner)
	batch := t.inner.BatchSize()
	bufs := make([][]byte, batch)
	for i := range bufs {
		bufs[i] = make([]byte, 65535)
	}
	sizes := make([]int, batch)
	for {
		n, err := t.inner.Read(bufs, sizes, 0)
		for i := range n {
			packet := append([]byte(nil), bufs[i][:sizes[i]]...)
			select {
			case t.from_inner <- packet:
			case <-t.closed:
				return
			}
		}
		if err != nil {
			if !errors.Is(err, tun.ErrTooManySegments) {
				t.inner_err = err
				return
			}
		}
	}
}

// Inject queues an outbound game packet for wireguard-go. It copies the
// packet. False means the queue is full or the device closed, and the
// packet is dropped.
func (t *split_tun) Inject(packet []byte) bool {
	select {
	case <-t.closed:
		return false
	default:
	}
	select {
	case t.injected <- append([]byte(nil), packet...):
		return true
	case <-t.closed:
		return false
	default:
		return false
	}
}

func (t *split_tun) take(packet []byte, buf []byte, sizes []int, i, offset int) {
	sizes[i] = copy(buf[offset:], packet)
}

// Read blocks for the first packet from either source, then fills the rest
// of the batch with whatever is already waiting.
func (t *split_tun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	select {
	case <-t.closed:
		return 0, os.ErrClosed
	default:
	}
	select {
	case packet, ok := <-t.from_inner:
		if !ok {
			return 0, t.read_error()
		}
		t.take(packet, bufs[0], sizes, 0, offset)
	case packet := <-t.injected:
		t.take(packet, bufs[0], sizes, 0, offset)
	case <-t.closed:
		return 0, os.ErrClosed
	}
	n := 1
	for n < len(bufs) {
		select {
		case packet, ok := <-t.from_inner:
			if !ok {
				return n, nil
			}
			t.take(packet, bufs[n], sizes, n, offset)
		case packet := <-t.injected:
			t.take(packet, bufs[n], sizes, n, offset)
		default:
			return n, nil
		}
		n++
	}
	return n, nil
}

func (t *split_tun) read_error() error {
	if t.inner_err != nil {
		return t.inner_err
	}
	return err_split_tun_closed
}

// Write offers each packet to the divert hook and writes the rest to the
// adapter. It reports every packet as written: a taken one was delivered,
// just not here.
func (t *split_tun) Write(bufs [][]byte, offset int) (int, error) {
	rest := bufs[:0:0]
	for _, buf := range bufs {
		if t.divert != nil && t.divert(buf[offset:]) {
			continue
		}
		rest = append(rest, buf)
	}
	if len(rest) > 0 {
		if _, err := t.inner.Write(rest, offset); err != nil {
			return 0, err
		}
	}
	return len(bufs), nil
}

func (t *split_tun) Close() error {
	t.close_once.Do(func() { close(t.closed) })
	return t.inner.Close()
}

func (t *split_tun) File() *os.File           { return t.inner.File() }
func (t *split_tun) MTU() (int, error)        { return t.inner.MTU() }
func (t *split_tun) Name() (string, error)    { return t.inner.Name() }
func (t *split_tun) Events() <-chan tun.Event { return t.inner.Events() }
func (t *split_tun) BatchSize() int           { return t.inner.BatchSize() }
