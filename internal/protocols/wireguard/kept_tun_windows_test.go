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
	}, clear: func(wintun_adapter) {}}
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
	created[0].start_err = errors.New("the adapter was removed in Device Manager")
	if _, _, err := k.session("Singularity"); err != nil {
		t.Fatalf("session() error = %v, want a new adapter in place of the broken one", err)
	}
	if len(created) != 2 || created[0].closes != 1 || created[1].starts != 1 {
		t.Fatalf("created %d adapters, the broken one closed %d times; want it closed once and a second one carrying the session", len(created), created[0].closes)
	}
}
