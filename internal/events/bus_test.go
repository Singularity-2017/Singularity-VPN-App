package events

import "testing"

func Test_publish_with_no_subscribers_does_not_panic(t *testing.T) {
	bus := New_bus(4)
	defer bus.Close()

	bus.Publish(Event{Kind: Kind_state_changed})
}

func Test_two_subscribers_each_receive_the_same_event(t *testing.T) {
	bus := New_bus(4)
	defer bus.Close()

	first, unsubscribe_first := bus.Subscribe()
	defer unsubscribe_first()
	second, unsubscribe_second := bus.Subscribe()
	defer unsubscribe_second()

	sent := Event{Kind: Kind_connection_started, Session_id: "session-1"}
	bus.Publish(sent)

	select {
	case got := <-first:
		if got != sent {
			t.Fatalf("first subscriber got %+v, want %+v", got, sent)
		}
	default:
		t.Fatal("first subscriber received nothing")
	}

	select {
	case got := <-second:
		if got != sent {
			t.Fatalf("second subscriber got %+v, want %+v", got, sent)
		}
	default:
		t.Fatal("second subscriber received nothing")
	}
}

func Test_unsubscribe_stops_delivery_and_closes_the_channel(t *testing.T) {
	bus := New_bus(4)
	defer bus.Close()

	ch, unsubscribe := bus.Subscribe()
	unsubscribe()

	bus.Publish(Event{Kind: Kind_connection_lost})

	got, open := <-ch
	if open {
		t.Fatalf("channel still open after unsubscribe, got %+v", got)
	}
}

func Test_unsubscribe_twice_is_safe(t *testing.T) {
	bus := New_bus(4)
	defer bus.Close()

	_, unsubscribe := bus.Subscribe()
	unsubscribe()
	unsubscribe()
}

func Test_a_slow_subscriber_does_not_block_publish_and_is_counted_in_dropped(t *testing.T) {
	bus := New_bus(1)
	defer bus.Close()

	_, unsubscribe := bus.Subscribe()
	defer unsubscribe()

	// The buffer holds one event; the second and third must be dropped
	// rather than block Publish, since nobody is reading.
	bus.Publish(Event{Kind: Kind_connection_started})
	bus.Publish(Event{Kind: Kind_connection_established})
	bus.Publish(Event{Kind: Kind_connection_degraded})

	if got := bus.Dropped(); got != 2 {
		t.Fatalf("Dropped() = %d, want 2", got)
	}
}

func Test_close_is_idempotent(t *testing.T) {
	bus := New_bus(4)

	bus.Close()
	bus.Close()
}

func Test_publish_after_close_does_not_panic(t *testing.T) {
	bus := New_bus(4)
	bus.Close()

	bus.Publish(Event{Kind: Kind_error})
}
