package core

import (
	"context"
	"maps"
	"net/netip"
	"slices"
	"sync"
	"time"

	"singularity-vpn-app/internal/ipc"
)

// ping_timeout bounds one Ping_servers call, and with it every echo and every
// name lookup inside it, which all start together. The window asks once a
// second while its server menu is open, so an answer that took longer than
// this would be overtaken by the next question.
const ping_timeout = 900 * time.Millisecond

// Ping_servers answers Command_ping_servers: how long each server the last
// Servers call listed takes to answer an ICMP echo, all of them measured at
// once.
//
// Only a server that answered is in the result. One that did not -- no
// address to measure, a name that did not resolve, an echo that timed out or
// was refused -- is absent, never present with a zero: a number on the screen
// is a measurement or it is not there at all.
//
// It needs no token and does not ask the control plane. The addresses come
// from the last Servers call, so before there has been one the result is
// empty, which is an answer and not an error. The result carries node ids and
// round trips only; the window never sees an address.
func (a *Application) Ping_servers(ctx context.Context) (ipc.Ping_servers_result, error) {
	a.hosts_mu.Lock()
	hosts := maps.Clone(a.hosts)
	a.hosts_mu.Unlock()

	result := ipc.Ping_servers_result{Pings: []ipc.Server_ping{}}
	if len(hosts) == 0 {
		return result, nil
	}

	ping_ctx, cancel := context.WithTimeout(ctx, ping_timeout)
	defer cancel()

	// Sorted ids, and one slot per id for its goroutine to write, give the
	// result its order without a lock and without sorting afterwards.
	node_ids := slices.Sorted(maps.Keys(hosts))
	rtts := make([]time.Duration, len(node_ids))
	var wg sync.WaitGroup
	for i, node_id := range node_ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rtts[i] = a.ping_host(ping_ctx, hosts[node_id])
		}()
	}
	wg.Wait()

	// A caller that has gone away is owed no answer, and what was measured
	// while it was going is not worth showing to the next one.
	if err := ctx.Err(); err != nil {
		return ipc.Ping_servers_result{}, err
	}

	for i, node_id := range node_ids {
		if rtts[i] > 0 {
			result.Pings = append(result.Pings, ipc.Server_ping{
				Node_id: node_id,
				Rtt_ms:  float64(rtts[i]) / float64(time.Millisecond),
			})
		}
	}
	return result, nil
}

// ping_host measures one server and returns its round trip, or zero when
// there is none to report. A host that is not an IP literal is resolved to
// IPv4 first and its first address is the one measured; a name that does not
// resolve is a server that did not answer.
//
// A round trip of zero or less from the pinger is treated as no answer too: a
// real echo takes time, and passing a zero on would show the one thing this
// feature must never show.
func (a *Application) ping_host(ctx context.Context, host string) time.Duration {
	address, err := netip.ParseAddr(host)
	if err != nil {
		addresses, err := a.resolve_ip4(ctx, host)
		if err != nil || len(addresses) == 0 {
			return 0
		}
		address = addresses[0]
	}
	rtt, err := a.pinger.Ping(ctx, address)
	if err != nil || rtt <= 0 {
		return 0
	}
	return rtt
}
