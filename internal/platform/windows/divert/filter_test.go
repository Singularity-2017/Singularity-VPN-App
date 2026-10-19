package divert

import "testing"

func Test_outbound_ports_filter(t *testing.T) {
	got, err := Outbound_ports_filter([]Port{{Udp, 7086}, {Tcp, 50000}, {Udp, 7086}})
	if err != nil {
		t.Fatal(err)
	}
	want := "outbound and !loopback and ip and ((tcp and tcp.SrcPort == 50000) or (udp and udp.SrcPort == 7086))"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func Test_no_ports_matches_nothing(t *testing.T) {
	got, err := Outbound_ports_filter(nil)
	if err != nil || got != "false" {
		t.Fatalf("got %q, %v; want \"false\"", got, err)
	}
}

func Test_a_bad_port_is_refused(t *testing.T) {
	for _, port := range []Port{{"icmp", 1}, {Udp, 0}} {
		if _, err := Outbound_ports_filter([]Port{port}); err == nil {
			t.Errorf("%+v: accepted", port)
		}
	}
}
