package connection

import (
	"strings"
	"testing"
)

func Test_default_session_id_produces_a_32_character_hex_string(t *testing.T) {
	id := default_session_id()
	if len(id) != 32 {
		t.Fatalf("default_session_id() = %q, want 32 hex characters", id)
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("default_session_id() = %q, contains a non-hex character %q", id, r)
		}
	}
}

func Test_default_session_id_is_not_repeated_across_calls(t *testing.T) {
	first := default_session_id()
	second := default_session_id()
	if first == second {
		t.Fatalf("default_session_id() returned %q twice", first)
	}
}

func Test_session_id_usable_refuses_empty_and_the_failure_sentinel(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"", false},
		{session_id_generation_failed, false},
		{"a1b2c3", true},
	}
	for _, c := range cases {
		if got := session_id_usable(c.id); got != c.want {
			t.Errorf("session_id_usable(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}
