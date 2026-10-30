//go:build windows

package wireguard

import (
	"errors"
	"testing"

	"golang.zx2c4.com/wintun"
)

type fake_wintun struct {
	start_err      error
	starts, closes int
}

func (f *fake_wintun) StartSession(uint32) (wintun.Session, error) {
	f.starts++
	return wintun.Session{}, f.start_err
}

func (f *fake_wintun) LUID() uint64 { return 1 }

func (f *fake_wintun) Close() error {
	f.closes++
	return nil
}

func fake_kept(created *[]*fake_wintun) *kept_adapters {
	return &kept_adapters{adapters: map[string]*kept_adapter{}, create: func(string) (wintun_adapter, error) {
		adapter := &fake_wintun{}
		*created = append(*created, adapter)
		return adapter, nil
	}, clear: func(wintun_adapter) {}, end_session: func(wintun.Session) {}}
}

func Test_an_adapter_that_could_not_be_created_is_tried_again_next_time(t *testing.T) {
	var created []*fake_wintun
	k := fake_kept(&created)
	create := k.create
	k.create = func(string) (wintun_adapter, error) { return nil, errors.New("wintun.dll was not found") }
	if _, _, err := k.session("Singularity"); err == nil {
		t.Fatal("session() succeeded with no adapter")
	}
	k.create = create
	if _, _, err := k.session("Singularity"); err != nil || len(created) != 1 {
		t.Fatalf("session() error = %v with %d adapters created, want the next try to create one", err, len(created))
	}
}

// Creating an adapter is a device installation the whole machine pays for;
// a reconnect, a switch or a boost only starts another session on it.
func Test_a_reconnect_reuses_the_adapter_instead_of_installing_another(t *testing.T) {
	var created []*fake_wintun
	k := fake_kept(&created)
	held, _, err := k.session("Singularity")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := k.session("Singularity"); err == nil {
		t.Fatal("a second session was started on an adapter already carrying a tunnel")
	}
	k.release(held)
	k.end("Singularity")
	if _, _, err := k.session("Singularity"); err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || created[0].closes != 0 || created[0].starts != 2 {
		t.Fatalf("created %d adapters, the first closed %d times with %d sessions; want one adapter, never closed, two sessions", len(created), created[0].closes, created[0].starts)
	}
}

func Test_an_adapter_that_no_longer_starts_sessions_is_replaced(t *testing.T) {
	var created []*fake_wintun
	k := fake_kept(&created)
	held, _, err := k.session("Singularity")
	if err != nil {
		t.Fatal(err)
	}
	k.release(held)
	k.end("Singularity")
	created[0].start_err = errors.New("the adapter was removed in Device Manager")
	if _, _, err := k.session("Singularity"); err != nil {
		t.Fatalf("session() error = %v, want a new adapter in place of the broken one", err)
	}
	if len(created) != 2 || created[0].closes != 1 || created[1].starts != 1 {
		t.Fatalf("created %d adapters, the broken one closed %d times; want it closed once and a second one carrying the session", len(created), created[0].closes)
	}
}

// A tunnel handed over to the next on the same adapter leaves its session
// running, so the adapter never goes down and Windows sees nothing change.
// Nothing is cleared off it either: what is on it is the next tunnel's to
// change.
func Test_a_session_left_running_is_carried_to_the_next_tunnel(t *testing.T) {
	var created []*fake_wintun
	k := fake_kept(&created)
	cleared, ended := 0, 0
	k.clear = func(wintun_adapter) { cleared++ }
	k.end_session = func(wintun.Session) { ended++ }
	held, _, err := k.session("Singularity")
	if err != nil {
		t.Fatal(err)
	}
	k.release(held)
	if _, _, err := k.session("Singularity"); err != nil {
		t.Fatal(err)
	}
	if created[0].starts != 1 || ended != 0 || cleared != 1 {
		t.Fatalf("%d sessions started, %d ended, the adapter cleared %d times; want the first session carried on, cleared once when it was new", created[0].starts, ended, cleared)
	}
}

// A session that ended under its tunnel -- the adapter disabled, the driver
// restarted -- is not handed to the next tunnel: its handle is freed, and a
// new session starts on the adapter, cleared of whatever the old one left.
func Test_a_session_lost_under_its_tunnel_is_replaced_by_a_new_one(t *testing.T) {
	var created []*fake_wintun
	k := fake_kept(&created)
	cleared, ended := 0, 0
	k.clear = func(wintun_adapter) { cleared++ }
	k.end_session = func(wintun.Session) { ended++ }
	held, _, err := k.session("Singularity")
	if err != nil {
		t.Fatal(err)
	}
	k.lose(held)
	k.release(held)
	if _, _, err := k.session("Singularity"); err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || created[0].starts != 2 || ended != 1 || cleared != 2 {
		t.Fatalf("%d adapters, %d sessions started, %d ended, cleared %d times; want the lost session ended and a new one on the cleared adapter",
			len(created), created[0].starts, ended, cleared)
	}
}

func Test_a_session_is_ended_only_when_no_tunnel_reads_it(t *testing.T) {
	var created []*fake_wintun
	k := fake_kept(&created)
	ended := 0
	k.end_session = func(wintun.Session) { ended++ }
	held, _, err := k.session("Singularity")
	if err != nil {
		t.Fatal(err)
	}
	k.end("Singularity")
	if ended != 0 {
		t.Fatal("ended the session a tunnel is still reading")
	}
	k.release(held)
	k.end("Singularity")
	k.end("Singularity")
	if ended != 1 {
		t.Fatalf("session ended %d times, want once", ended)
	}
}

// The service stopping takes the adapters off the machine, a session left
// running with them.
func Test_releasing_the_adapters_ends_a_session_left_running(t *testing.T) {
	var created []*fake_wintun
	k := fake_kept(&created)
	ended := 0
	k.end_session = func(wintun.Session) { ended++ }
	held, _, err := k.session("Singularity")
	if err != nil {
		t.Fatal(err)
	}
	k.release(held)
	k.release_all()
	if ended != 1 || created[0].closes != 1 || len(k.adapters) != 0 {
		t.Fatalf("ended %d sessions, closed the adapter %d times, %d adapters left; want the session ended and the adapter removed", ended, created[0].closes, len(k.adapters))
	}
}
