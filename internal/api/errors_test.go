package api

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func Test_error_message_includes_status_and_code(t *testing.T) {
	err := &Error{Status: 401, Code: Code_unauthorized, Message: "unauthorized"}
	got := err.Error()
	for _, want := range []string{"401", Code_unauthorized, "unauthorized"} {
		if !strings.Contains(got, want) {
			t.Fatalf("error message %q drops %q", got, want)
		}
	}
}

func Test_is_code_matches_only_the_named_code(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", &Error{Status: 403, Code: Code_device_token_cannot_mint})
	if !Is_code(err, Code_device_token_cannot_mint) {
		t.Fatalf("Is_code did not see the code through a wrapped error")
	}
	if Is_code(err, Code_forbidden) {
		t.Fatalf("Is_code matched a code the error does not carry")
	}
	if Is_code(errors.New("not an api error"), Code_forbidden) {
		t.Fatalf("Is_code matched a plain error")
	}
}
