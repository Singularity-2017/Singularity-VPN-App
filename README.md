[Русская версия](README.ru.md)

# Singularity VPN — the desktop application

One button. The application signs in once, asks the control plane where to
connect, brings the tunnel up and keeps it up. Nothing has to be installed
first: the tunnel ships inside the installer.

This repository holds the application. The control plane it talks to — the
HTTP API, the portal, the node management — is a separate repository,
`Singularity-VPN`.

## What is built here

| Binary | Runs as | Owns |
|---|---|---|
| `svpn-service.exe` | LocalSystem, a Windows service | The tunnel, routes, DNS, every call to the control plane, updates |
| `SingularityVPN.exe` | The logged-in user | The window and the tray icon, and nothing else |
| `svpn-swap.exe` | Launched by the service | Replacing files during an update. No network access |

Only `svpn-service.exe` has a `cmd` directory today, and all it does is print
what the build put into it. The other two arrive with the stages that need
them; the stage table is in
[the design document](docs/superpowers/specs/2026-09-15-singularity-vpn-app-design.md).

## Appearance and account

Settings → Theme keeps an independent selection for normal and gamer modes.
Normal buttons default to circles and capsules; gamer buttons default to rounded
squares. The editor offers square, round and rounded-square shapes, colors and
background patterns with immediate preview. Save As stores an independent named
theme; Reset restores the selected theme's defaults. Round game cards use one compact circular action surface with the game title
near its bottom; match questions sit below it. Toxin uses a
subtle molecular pattern rather than generic stripes.

The tariff badge sits below the application title and shows the published target
speed. Account, Help and Settings appear in that order. Tariff details show the
active subscription's expiry, profile limits, speed conditions and price when
available. Subscription and VPN-profile expiry are independent; free or sponsored
access does not show an inactive subscription's old date.
Credits also appear immediately left of the account icon. The account menu shows
credits, the profile selector, invitations, sponsored places and devices. Profile
selection closes the menu; successful profile changes and network changes appear
in the journal. The devices list marks the current device using its stored ID.
Network contribution contains spending and feedback history. Spending is the
control plane's total of this account's recorded paid payments, per currency,
and its payments page by page; the client never sums pages or allocations into
it. When the control plane cannot vouch for the whole history the total is
unavailable and the list shows only verified payments. A traversal the history
changed under, or whose cursor expired, starts over. Allocations to the tariff,
which may be paid by someone else, live under the tariff. Unknown values stay
absent; unavailable sections report an error. Account data is cleared on sign-out.
Renewal and other account changes remain in the web portal, opened without
passing the desktop authentication token to the browser.
Help opens FAQ and a feedback form. Failed submissions retain the draft; a
successful submission returns its receipt code. Connection warnings report the
actual measured cause (latency, failed TCP checks, or both) and available numbers.
When the connection fails repeatedly without a network change, a "report a
problem" button appears by the warning and opens the form with the message
already written. A problem report can carry diagnostics the service gathers
at that moment — the journal and the service log of the last two hours
(secrets and IP addresses masked), adapter types, MTU and DNS classes without
the machine's addresses, and optionally the programs that were in the
foreground. All of it is shown before sending. Journal and log lines an
earlier accepted report already carried are not sent again, across restarts
too. The report names the session the failures happened on, even when the
service has restarted since. The control plane counts a reported server and
protocol as a failed session on that network, and the next automatic choice
there prefers another usable offer.

## Connection measurements

The main button shows elapsed time below its connected label. Settings → Statistics
shows local totals: connected duration (including degraded periods), connections,
rebuilds, losses, switches, failures and observed TCP measurements. Measurements
start with this version; older history is not inferred. The service checkpoints
aggregate counters every 30 seconds and on shutdown, without account, endpoint or
network identifiers. A crash may lose the last checkpoint interval; offline time
is never counted. Corrupt history is preserved and reported as unavailable.

Full control probes start at a 5-second interval. Three healthy confirmations
permit each increase: 10, 20, 40, then at most 60 seconds. A failure, changed network
or new connection restores fast checks. Local tunnel health is still checked every
5 seconds. This is a bounded adaptive schedule, not traffic prediction; a failure
in the path while the local tunnel stays up can take up to one full interval to
notice. User actions cancel pending monitor probes before changing configuration.

The latency warning measures TCP connection establishment, not ICMP ping or game
latency. DNS lookup and socket-close time are excluded; a successful 1053 ms dial
remains 1053 ms. Slow checks record their minimum and maximum without endpoint
addresses, to distinguish a mixed sample from uniformly slow connections.

Services is visible in both layouts and expands in place. Its last direct DNS/TCP/TLS
check survives service restarts, with historical results explicitly marked. Unknown
checks are not counted as available. Refresh is available only with the VPN off;
these results do not describe reachability through the current VPN connection.

## Building

```
go build ./...
go test ./...
scripts/build.ps1
```

`go build` produces a binary that connects nowhere, on purpose. `build.ps1`
supplies the real values at link time.

## Releasing

Versions stay 0.x until the application reliably gets through
Roskomnadzor's blocking; that is 1.0. The first release is v0.1.0.

```
scripts/release.ps1 [-Minimum v1.2.0] [-Urgent] [-Mirror https://...]
```

`VERSION` is the single source of the base version (`X.Y.Z`): the release
being worked towards, not the last one out, so a build between releases reads
as a pre-release of the next (`v0.1.4-dev.<commit>` after `v0.1.3`). A release
tags that number; the commit after it raises `VERSION` to the next. Both scripts read that file; an optional
`-Version` must match it and cannot hide uncommitted changes. All binaries and
the setup receive the same version. Uncommitted changes, including new source
files, append `-dirty`; untracked agent-local files are excluded. A clean build
without the matching tag appends `-dev.<commit>` (or `-dev` without Git).
Older tags never select the base number.

The release script refuses an uncommitted tree, creates the matching tag,
builds with `-Release`, signs into `local/release-vX.Y.Z/` with
`local/release-keys/primary.key`, and verifies the result. Signing and verification
check that the setup's embedded version matches the manifest; dirty and
development builds cannot be signed as releases. Only a clean release version
participates in automatic updates. Neither script publishes automatically.

Two steps are left to whoever publishes, and the script prints both:
pushing the tag, and `deploy app-release local/release-v1.2.3` from the
control plane's repository. That command checks the release, puts it into
the control plane's `APP_RELEASE_DIR` without a half-published moment, and
verifies from outside that `/api/v1/app/update` serves exactly these
files. `deploy app-release --withdraw` stops a release from spreading;
installations that already took it stay on it, and the remedy is a higher
version.

## The control plane address is not in the source

`internal/buildinfo.Control_plane` carries an address from the RFC 5737
documentation range. The real address is supplied at link time from
`local/app.env`, which is git-ignored:

```
go build -ldflags "-X singularity-vpn-app/internal/buildinfo.Control_plane=$address" ./cmd/svpn-service
```

So a binary built from a clean clone connects nowhere, and anybody who forks
this project gets an honest starting point rather than somebody else's
server. A test enforces it: `Test_default_control_plane_is_a_documentation_address`
fails if a real address is ever committed.

## Documents

- [The design](docs/superpowers/specs/2026-09-15-singularity-vpn-app-design.md) —
  the decisions taken on 2026-09-15, and the stages.
- [CLIENT_CORE.md](docs/CLIENT_CORE.md) — what the client is, package by package.
- [WINDOWS_CLIENT.md](docs/WINDOWS_CLIENT.md) — the Windows specifics.
- [CLIENT_AUTO_SWITCH.md](docs/CLIENT_AUTO_SWITCH.md) — the retry budget and
  the switch to another protocol or node.
- [MINIMAL_CLIENT_CLI.md](docs/MINIMAL_CLIENT_CLI.md) — superseded by the
  design, kept because it records why.

## Issues

In Gitea, not on GitHub. See [docs/agents/issue-tracker.md](docs/agents/issue-tracker.md).

The compact main screen switches between game cards and the journal using Journal or Games, without changing gamer mode or an active game boost. Empty and unavailable journals are shown explicitly. The window height stays under the user’s control; long content scrolls without automatically moving or resizing the window. Journal/game replacements crossfade in 100 ms (except reduced motion). Sign-out requires confirmation reminding the user that signing in currently requires the password. Account names survive status replies that omit the username and clear on sign-out.
