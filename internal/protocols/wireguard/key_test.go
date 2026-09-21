package wireguard

import (
	"strings"
	"testing"
)

const a_valid_key_base64 = "MVge1nFDeRVlPsb9GmtIXuLlooewnRqaq9CXtIXO3lE="
const a_valid_key_hex = "31581ed671437915653ec6fd1a6b485ee2e5a287b09d1a9aabd097b485cede51"

func Test_parse_key_accepts_a_valid_base64_key(t *testing.T) {
	key, err := Parse_key(a_valid_key_base64)
	if err != nil {
		t.Fatalf("Parse_key: %v", err)
	}
	if key.Hex() != a_valid_key_hex {
		t.Fatalf("Hex() = %q, want %q", key.Hex(), a_valid_key_hex)
	}
	if key.Is_zero() {
		t.Fatal("a decoded key reports Is_zero() = true")
	}
}

func Test_parse_key_refuses_a_key_of_the_wrong_decoded_length(t *testing.T) {
	// "hggWgKFLlsGwS588XpmY/w==" decodes to 16 bytes, not 32.
	_, err := Parse_key("hggWgKFLlsGwS588XpmY/w==")
	if err == nil {
		t.Fatal("expected an error for a 16-byte key")
	}
	if !strings.Contains(err.Error(), "16") {
		t.Fatalf("err = %q, does not name the length it got", err)
	}
}

func Test_parse_key_refuses_invalid_base64(t *testing.T) {
	_, err := Parse_key("not valid base64!!")
	if err == nil {
		t.Fatal("expected an error for invalid base64")
	}
}

func Test_zero_key_is_zero(t *testing.T) {
	var key Key
	if !key.Is_zero() {
		t.Fatal("the zero Key reports Is_zero() = false")
	}
}

// This is the guard on the whole no-leak rule: a key with known bytes must
// never show its base64 or its hex through String().
func Test_key_string_does_not_contain_the_key_material(t *testing.T) {
	key, err := Parse_key(a_valid_key_base64)
	if err != nil {
		t.Fatalf("Parse_key: %v", err)
	}
	rendered := key.String()
	if strings.Contains(rendered, a_valid_key_base64) {
		t.Fatalf("String() = %q, contains the base64 key material", rendered)
	}
	if strings.Contains(rendered, a_valid_key_hex) {
		t.Fatalf("String() = %q, contains the hex key material", rendered)
	}
}
