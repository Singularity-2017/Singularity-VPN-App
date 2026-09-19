package events

import "sync"

// default_buffer is used when New_bus is asked for a buffer that makes no
// sense as a channel size.
const default_buffer = 64

// Bus is the one place an event passes through on its way from the
// goroutine that owns the tunnel to whoever is listening -- the user
// interface over the pipe, and the log.
//
// Publish never blocks: it is called from the goroutine that owns the
// tunnel, and a user interface that stopped reading must not be able to
// freeze a VPN connection. A subscriber whose channel is full is skipped
// rather than waited for. The skip is not forgotten -- Dropped counts it --
// because a subscriber that has fallen behind must re-read the current
// state rather than assume it saw every event; that re-read is the contract
// this design buys in exchange for never blocking.
type Bus struct {
	mu          sync.Mutex
	buffer      int
	subscribers map[int]chan Event
	next_id     int
	dropped     uint64
	closed      bool
}

// New_bus creates a bus whose subscribers each buffer up to buffer events
// before Publish starts skipping them. buffer <= 0 means a small sane
// default.
func New_bus(buffer int) *Bus {
	if buffer <= 0 {
		buffer = default_buffer
	}
	return &Bus{
		buffer:      buffer,
		subscribers: make(map[int]chan Event),
	}
}

// Subscribe registers a new listener and returns the channel it receives
// events on, along with a function that unsubscribes and closes that
// channel. The returned function is safe to call more than once.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	id := b.next_id
	b.next_id++
	channel := make(chan Event, b.buffer)
	if !b.closed {
		b.subscribers[id] = channel
	} else {
		close(channel)
	}
	b.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			b.mu.Lock()
			if _, subscribed := b.subscribers[id]; subscribed {
				delete(b.subscribers, id)
				close(channel)
			}
			b.mu.Unlock()
		})
	}
	return channel, unsubscribe
}

// Publish delivers event to every current subscriber without blocking. A
// subscriber whose channel is full is skipped and counted in Dropped
// instead. Publish after Close is a no-op rather than a panic, because
// shutdown races are normal and must not take the process down.
func (b *Bus) Publish(event Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return
	}
	for _, channel := range b.subscribers {
		select {
		case channel <- event:
		default:
			b.dropped++
		}
	}
}

// Dropped returns how many deliveries were skipped because a subscriber's
// channel was full. Silent loss is the thing this package exists to avoid,
// so the loss is at least countable.
func (b *Bus) Dropped() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}

// Close closes every subscriber channel. It is idempotent.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return
	}
	b.closed = true
	for id, channel := range b.subscribers {
		close(channel)
		delete(b.subscribers, id)
	}
}
