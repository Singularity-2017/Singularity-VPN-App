[Русская версия](0001-control-plane-address.ru.md)

# ADR 0001 — The control plane address outlives the build

- Status: proposed
- Date: 2026-10-28

## Context

The control plane address is compiled into the binary
(`buildinfo.Control_plane`, `internal/buildinfo/buildinfo.go:14`) and read
directly in four places:

- `cmd/svpn-service/service_windows.go:450` — the API client (`api.New`);
- `cmd/svpn-service/service_windows.go:541` — network context capture
  (`winplatform.Capture_network`);
- `cmd/svpn-service/update_windows.go:44` — the control plane update channel;
- `cmd/singularityvpn/portal.go:14` — the account portal link in the window.

`api.Client` holds one `base_url` for its lifetime
(`internal/api/control_plane.go:29`). When the control plane moves — the
hosting provider re-addresses it, or we migrate it, as on 2026-10-16 — every
installed copy keeps calling the old address. An update does not rescue it
either while the manifest is fetched from that same address first.

What already exists and does not depend on the control plane address:

- A signed release manifest (`internal/update/manifest.go:36`), Ed25519,
  verified against keys compiled into the build
  (`update.Trusted_keys`); the private key exists only on the build machine.
- Channels independent of the control plane: mirrors named by the newest
  verified manifest (`State.Mirrors`, `internal/update/updater.go:233`) and a
  compiled fallback mirror on the code host (`buildinfo.Update_mirror`).
- Replay protection: `State.Highest_version`; for the same version the
  later `released_at` wins (`internal/update/updater.go:223`).
- Unknown manifest fields are ignored (`manifest.go`, `Parse`), so a new
  field reaches old builds harmlessly.
- Checks run every six hours (`Check_interval`, `updater.go:93`).

On the server side the nodes already track the control plane's current
address (`/etc/singularity/control-addr`), but do not hand it to clients.

## What is at stake

The application sends the control plane a bearer token. Whoever the
application believes is the control plane receives that token and can act
as the user, and also decides which nodes and profiles the user connects
through.

## Threat model

Adversaries this decision must hold against:

1. **The new holder of an old address.** A provider re-addresses our server
   and later gives the old IP to another customer. Every copy still
   configured with it sends its token there. This is the threat the
   re-addressing scenario creates by itself, with no attacker effort.
2. **A forger of "the control plane is now there".** Anyone who can answer
   one of the application's requests — a mirror, a node, a captive portal, a
   censor's middlebox — tries to redirect the application to an address they
   hold.
3. **A replayer.** Someone serves an old, genuinely signed statement that
   names an address we have since given up (it then falls under 1).

Explicitly out of scope here, and stated so as not to overclaim:

- **An on-path observer of the current control plane traffic.** The
  production control plane is reached over plain `http`
  (`local/app.env`), so the bearer token already crosses the network in
  clear text on every request. No choice in this ADR makes that better or
  worse. Moving the control plane to TLS with a certificate the application
  pins is a separate decision, flagged in Consequences; it is the larger
  risk of the two.
- Compromise of the build machine's signing key: it already lets an
  attacker ship arbitrary code, so trusting it for an address adds nothing.

Requirement derived from the above: **an address is accepted only from a
statement signed by a key compiled into the build, newer than the
statement that set the current address; an address that a newer statement
dropped is never contacted again.**

## Options

1. **Control plane addresses in the signed manifest.** A new manifest field
   lists the control plane's base URLs. Trust equals that of updates; the
   manifest is reachable through mirrors and the code host without the
   control plane. Cost: the owner republishes the manifest after a move
   (`deploy.exe app-release`); clients pick it up within six hours, or at
   once if the control plane stops answering (see Decision).
2. **Ask a node from the last profiles.** Automatic, but a node's answer is
   unauthenticated. Making it safe needs a control plane identity key
   pinned in the application, the control plane signing its own address
   with it, nodes relaying that signed record, and an HTTP endpoint for
   clients on every node, which nodes do not have. Changes in two
   repositories and on every node.
3. **Nodes proxy `/vpn/api/` to the control plane.** Every node becomes an
   API entry point, which also helps when the control plane's address is
   blocked from Russia. Most expensive: a proxy on each node, and without
   end-to-end TLS every node can read every token, which widens threat 2
   rather than closing it.

A DNS name is not considered: the owner rejects depending on an external
service.

## Decision

**Option 1.** It is the only one that meets the requirement with keys and
channels the application already has, and the only one that is one-sided
in the application plus a small change to the release tool.

Details:

- **Field.** `control_planes: [base URL, ...]` in the manifest, up to 4,
  each an absolute `http`/`https` URL with no query, fragment or user info.
  `http` stays allowed only because production is `http` today; when the
  control plane moves to TLS, the release that pins it narrows this to
  `https`.
- **Freshness.** The stored list remembers the `released_at` of the
  manifest it came from. A manifest replaces the list only if its
  `released_at` is strictly later. A manifest without the field changes
  nothing (an empty list is not "no control plane"). This closes threat 3:
  an old manifest cannot bring back an address a newer one dropped.
- **No fallback to the compiled address.** Once a signed list is stored,
  the compiled address is not contacted any more — falling back to it is
  exactly threat 1. The compiled address is only the first value, used
  while nothing is stored.
- **Where it lives.** One file, `control-plane.json` in the service's
  private data directory (`%ProgramData%\Singularity VPN`, writable only by
  LocalSystem and Administrators, like `update\state.json`). Written
  atomically. A file that is missing or does not parse means "nothing
  stored", logged; it never means "no control plane".
- **Who owns it.** A small package (`internal/control_address`) owns
  `Load`, `Current`, `Offer(list, released_at)` and change notification.
  The updater calls `Offer` after a manifest verifies; the API client, the
  network capture and the updater's own control plane channel read
  `Current`. The window gets the address for the portal link from the
  service over IPC instead of from `buildinfo`.
- **Switching at run time.** `api.Client` reads its base URL per request
  from a source instead of a fixed string, so a change takes effect without
  restarting the service. With several addresses, requests go to the first;
  the one that last answered moves to the front. This is failover between
  addresses we signed, not discovery.
- **Faster than six hours.** When the control plane has failed to answer
  for N consecutive attempts (proposed: 3, at least 10 minutes apart), the
  service asks the updater for an early check against the mirrors only.
  Rate-limited to once an hour so an outage does not hammer the mirrors.
- **Journal.** A change of address is a journal line in plain Russian
  ("Сервер управления переехал, адрес обновлён из подписанного манифеста"),
  with no address in it.

## Consequences

- A move no longer strands installations, provided the owner republishes
  the manifest; at least one mirror or the code host must be reachable.
- A copy that misses the window between the move and the old IP's reuse
  is protected anyway: once it has stored a signed list it never returns to
  the compiled address; before that, the compiled address is the only
  address it ever had, so nothing is lost relative to today.
- The owner has a manual step on every move. `deploy.exe migrate` should
  print it.
- Copies built before this change ignore the field and stay stranded; only
  an update reaches them, and the update itself reaches them through
  mirrors.
- **Not solved here:** the token in clear text over `http`; a control plane
  address blocked from Russia. Option 3, or option 2 with a pinned identity
  key, remains the path for the second; TLS with a pinned certificate is
  the fix for the first and should be the next ADR.

## Server-side work (for the VPN repository session)

1. **Manifest field.** The release tool (`deploy.exe app-release`) writes
   `control_planes` into the manifest from the control plane's current
   address, before signing. It must refuse addresses from loopback, private
   and documentation ranges.
2. **Republish without a new version.** A way to re-sign and publish the
   current version's manifest with a new `released_at` and the new
   `control_planes`, to every channel (control plane, mirrors, code host
   release assets), without building a new setup.
3. **Migration reminder.** `deploy.exe migrate` ends by printing the
   republish command from item 2.
4. **Control plane serves the field.** `/api/v1/app/update` and `.sig`
   serve the republished manifest byte for byte (already the case if it
   serves the stored file).
5. Optional, not needed for this ADR: TLS for the control plane API, which
   the next ADR will need.
