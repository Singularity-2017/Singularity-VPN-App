package wireguard

import (
	"testing"

	"singularity-vpn-app/internal/protocols"
)

// The "amneziawg" registration is the same adapter: it keeps the optional
// abilities the manager looks for, and sits beside "wireguard" in one
// registry.
func Test_amnezia_is_the_same_adapter_under_its_own_name(t *testing.T) {
	a, _ := games_adapter(t, &fake_router{}, &fake_network{})
	var amnezia protocols.Protocol = Amnezia{Adapter: a}
	if amnezia.Name() != "amneziawg" {
		t.Fatalf("name = %q", amnezia.Name())
	}
	if _, ok := amnezia.(protocols.Repinner); !ok {
		t.Fatal("lost Repin")
	}
	if _, ok := amnezia.(protocols.Transport_reporter); !ok {
		t.Fatal("lost Transport_report")
	}
	registry, err := protocols.New_registry(a, amnezia)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"wireguard", "amneziawg"} {
		if _, err := registry.Get(name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
