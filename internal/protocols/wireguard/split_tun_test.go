package wireguard

import (
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/tun"
)

// fake_tun is an adapter whose reads come from a channel and whose writes
// are recorded.
type fake_tun struct {
	reads   chan []byte
	mu      sync.Mutex
	written [][]byte
	closed  chan struct{}
}

func new_fake_tun() *fake_tun {
	return &fake_tun{reads: make(chan []byte, 8), closed: make(chan struct{})}
}

func (f *fake_tun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	select {
	case packet := <-f.reads:
		sizes[0] = copy(bufs[0][offset:], packet)
		return 1, nil
	case <-f.closed:
		return 0, os.ErrClosed
	}
}

func (f *fake_tun) Write(bufs [][]byte, offset int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, buf := range bufs {
		f.written = append(f.written, append([]byte(nil), buf[offset:]...))
	}
	return len(bufs), nil
}

func (f *fake_tun) Close() error             { close(f.closed); return nil }
func (f *fake_tun) File() *os.File           { return nil }
func (f *fake_tun) MTU() (int, error)        { return 1420, nil }
func (f *fake_tun) Name() (string, error)    { return "fake", nil }
func (f *fake_tun) Events() <-chan tun.Event { return nil }
func (f *fake_tun) BatchSize() int           { return 4 }

func read_one(t *testing.T, s *split_tun) []byte {
	t.Helper()
	bufs := [][]byte{make([]byte, 100), make([]byte, 100)}
	sizes := make([]int, 2)
	done := make(chan int, 1)
	go func() {
		n, _ := s.Read(bufs, sizes, 16)
		done <- n
	}()
	select {
	case n := <-done:
		if n < 1 {
			t.Fatal("read returned nothing")
		}
		return bufs[0][16 : 16+sizes[0]]
	case <-time.After(2 * time.Second):
		t.Fatal("read blocked")
	}
	return nil
}

func Test_an_injected_packet_is_read_while_the_adapter_is_idle(t *testing.T) {
	inner := new_fake_tun()
	s := new_split_tun(inner, nil)
	defer s.Close()
	game := []byte("game packet")
	if !s.Inject(game) {
		t.Fatal("inject refused")
	}
	game[0] = 'X' // Inject copied it
	if got := read_one(t, s); string(got) != "game packet" {
		t.Fatalf("read %q", got)
	}
}

func Test_adapter_packets_still_reach_wireguard(t *testing.T) {
	inner := new_fake_tun()
	s := new_split_tun(inner, nil)
	defer s.Close()
	inner.reads <- []byte("browser packet")
	if got := read_one(t, s); string(got) != "browser packet" {
		t.Fatalf("read %q", got)
	}
}

func Test_writes_the_hook_takes_never_reach_the_adapter(t *testing.T) {
	inner := new_fake_tun()
	var taken []string
	s := new_split_tun(inner, func(packet []byte) bool {
		if string(packet) == "game reply" {
			taken = append(taken, string(packet))
			return true
		}
		return false
	})
	defer s.Close()
	bufs := [][]byte{append(make([]byte, 16), "game reply"...), append(make([]byte, 16), "web reply"...)}
	n, err := s.Write(bufs, 16)
	if err != nil || n != 2 {
		t.Fatalf("Write = %d, %v; want both reported written", n, err)
	}
	if !slices.Equal(taken, []string{"game reply"}) {
		t.Fatalf("hook took %v", taken)
	}
	if len(inner.written) != 1 || string(inner.written[0]) != "web reply" {
		t.Fatalf("adapter got %q", inner.written)
	}
}

func Test_a_closed_device_stops_reads_and_injects(t *testing.T) {
	inner := new_fake_tun()
	s := new_split_tun(inner, nil)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s.Inject([]byte("late")) {
		t.Fatal("a closed device took a packet")
	}
	_, err := s.Read([][]byte{make([]byte, 10)}, make([]int, 1), 0)
	if err == nil {
		t.Fatal("a closed device kept reading")
	}
	if !errors.Is(err, os.ErrClosed) && !errors.Is(err, err_split_tun_closed) {
		t.Fatalf("read error = %v", err)
	}
}
