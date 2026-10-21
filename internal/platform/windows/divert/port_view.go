package divert

import "time"

// port_view is what the capture path knows about local ports at one
// moment: which belong to a game, and which belong to anybody it has heard
// of. The owner table is kept on another goroutine; it publishes a fresh
// view after every change, and the capture path only ever reads one.
type port_view struct {
	game  map[Port]bool
	known map[Port]bool
}

func new_port_view(game, known []Port) *port_view {
	v := &port_view{game: make(map[Port]bool, len(game)), known: make(map[Port]bool, len(known))}
	for _, p := range game {
		v.game[p] = true
	}
	for _, p := range known {
		v.known[p] = true
	}
	return v
}

// The wait for a port nobody has heard of. A socket's bind reaches the
// owner table through the socket layer and another goroutine, and the
// first packet from it can arrive here a moment before: that packet is
// held until the table catches up rather than sent out direct, which would
// give the game's server a second address for the flow.
//
// Every outbound packet of the machine passes the one goroutine that
// holds, so holding is rationed: only a packet that can open a flow is
// held (a TCP SYN, any UDP datagram -- a TCP segment past its SYN belongs
// to a flow already decided), a port is decided once and remembered, so the
// FIN and RST of a socket the table has already dropped go straight
// through, and all the holding together may take no more than hold_budget
// of each second. Past that, an unknown port goes direct at once.
const (
	unknown_wait = 20 * time.Millisecond
	unknown_step = time.Millisecond
	hold_budget  = 100 * time.Millisecond
	// forget_every matches the router's refresh, when the table reads the
	// system's socket tables again and may learn a port it missed.
	forget_every = 30 * time.Second
)

// port_judge decides, packet by packet, whether a local port is a game's.
// It belongs to the one capture goroutine and is not safe to share.
type port_judge struct {
	load  func() *port_view
	sleep func(time.Duration)
	now   func() time.Time
	// decided is each port's last verdict, kept after the table forgets
	// the port so a closed socket's last packets need no wait; false
	// also stands for "waited in vain". It is forgotten every
	// forget_every, when a port number may well have a new owner.
	decided    map[Port]bool
	decided_at time.Time
	held       time.Duration // holding spent in the second that began at held_from
	held_from  time.Time
}

func new_port_judge(load func() *port_view) *port_judge {
	return &port_judge{load: load, sleep: time.Sleep, now: time.Now, decided: map[Port]bool{}}
}

// is_game reports whether port is a game's. opens says the packet can
// open a flow, and so may be held for a port not heard of yet.
func (j *port_judge) is_game(port Port, opens bool) bool {
	now := j.now()
	if now.Sub(j.decided_at) > forget_every {
		clear(j.decided)
		j.decided_at = now
	}
	view := j.load()
	if view != nil && view.known[port] {
		game := view.game[port]
		j.decided[port] = game
		return game
	}
	if game, ok := j.decided[port]; ok {
		return game
	}
	if view == nil || !opens {
		return false
	}
	if now.Sub(j.held_from) >= time.Second {
		j.held, j.held_from = 0, now
	}
	start := now
	for j.held < hold_budget {
		j.sleep(unknown_step)
		at := j.now()
		j.held += at.Sub(now)
		now = at
		if view = j.load(); view != nil && view.known[port] {
			game := view.game[port]
			j.decided[port] = game
			return game
		}
		if now.Sub(start) >= unknown_wait {
			break
		}
	}
	j.decided[port] = false
	return false
}
