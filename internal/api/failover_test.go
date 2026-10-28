package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// counting answers every request with a login token and counts them.
func counting(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		write_success(w, `{"token":"tok"}`)
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

// dropping accepts the connection and closes it unanswered: a transport
// failure, the way a re-addressed host looks from here.
func dropping(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

type source struct {
	mu   sync.Mutex
	list []string
}

func (s *source) get() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.list...)
}

func (s *source) set(list ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = list
}

func Test_a_changed_address_is_used_by_the_next_request(t *testing.T) {
	old, old_hits := counting(t)
	moved, moved_hits := counting(t)
	addresses := &source{list: []string{old.URL}}
	client, err := New(Config{Base_urls: addresses.get, Sleep: no_sleep})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Login(context.Background(), "u", "p"); err != nil {
		t.Fatal(err)
	}
	addresses.set(moved.URL + "/")
	if _, err := client.Login(context.Background(), "u", "p"); err != nil {
		t.Fatal(err)
	}
	if old_hits.Load() != 1 || moved_hits.Load() != 1 {
		t.Fatalf("hits old %d moved %d, want 1 and 1", old_hits.Load(), moved_hits.Load())
	}
}

func Test_an_address_that_does_not_answer_gives_way_to_the_next_and_stays_behind(t *testing.T) {
	dead, dead_hits := dropping(t)
	alive, alive_hits := counting(t)
	client, err := New(Config{Base_urls: func() []string { return []string{dead.URL, alive.URL} }, Sleep: no_sleep})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := client.Login(context.Background(), "u", "p"); err != nil {
			t.Fatalf("Login %d: %v", i, err)
		}
	}
	// The first request found the dead address; the second went straight
	// to the one that answered.
	if dead_hits.Load() != 1 || alive_hits.Load() != 2 {
		t.Fatalf("hits dead %d alive %d, want 1 and 2", dead_hits.Load(), alive_hits.Load())
	}
}

// A request that must not be repeated goes to one address only: a transport
// error after sending says nothing about whether the server acted on it.
func Test_a_request_that_must_not_repeat_tries_one_address(t *testing.T) {
	dead, dead_hits := dropping(t)
	alive, alive_hits := counting(t)
	client, err := New(Config{Base_urls: func() []string { return []string{dead.URL, alive.URL} }, Sleep: no_sleep})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Account(context.Background(), "tok"); err == nil {
		t.Fatal("Account succeeded through a dead address")
	}
	if dead_hits.Load() != 1 || alive_hits.Load() != 0 {
		t.Fatalf("hits dead %d alive %d, want 1 and 0", dead_hits.Load(), alive_hits.Load())
	}
}

func Test_new_refuses_a_source_and_a_fixed_address_together(t *testing.T) {
	if _, err := New(Config{Base_url: "http://192.0.2.1", Base_urls: func() []string { return nil }}); err == nil {
		t.Fatal("New accepted both Base_url and Base_urls")
	}
}

func Test_an_empty_source_fails_the_request_not_the_process(t *testing.T) {
	client, err := New(Config{Base_urls: func() []string { return nil }, Sleep: no_sleep})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Login(context.Background(), "u", "p"); err == nil {
		t.Fatal("Login succeeded with no address")
	}
}

func Test_answered_reports_whether_any_address_answered(t *testing.T) {
	dead, _ := dropping(t)
	alive, _ := counting(t)
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	t.Cleanup(failing.Close)
	cases := []struct {
		name  string
		bases []string
		want  bool
	}{
		{"every address dropped", []string{dead.URL}, false},
		{"the second answered", []string{dead.URL, alive.URL}, true},
		// An HTTP error is still an answer: the control plane is there.
		{"an error status", []string{failing.URL}, true},
	}
	for _, c := range cases {
		var heard []bool
		client, err := New(Config{Base_urls: func() []string { return c.bases }, Sleep: no_sleep,
			Answered: func(ok bool) { heard = append(heard, ok) }})
		if err != nil {
			t.Fatal(err)
		}
		client.Login(context.Background(), "u", "p")
		if len(heard) == 0 || heard[len(heard)-1] != c.want {
			t.Errorf("%s: heard %v, want last %v", c.name, heard, c.want)
		}
	}
}

func Test_a_cancelled_request_reports_nothing(t *testing.T) {
	dead, _ := dropping(t)
	var heard []bool
	client, _ := New(Config{Base_urls: func() []string { return []string{dead.URL} }, Sleep: no_sleep,
		Answered: func(ok bool) { heard = append(heard, ok) }})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client.Login(ctx, "u", "p")
	if len(heard) != 0 {
		t.Fatalf("heard %v, want nothing", heard)
	}
}
