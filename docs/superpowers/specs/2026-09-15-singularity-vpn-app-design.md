[Русская версия](2026-09-15-singularity-vpn-app-design.ru.md)

# Singularity VPN desktop application — design

## Status

This document records the decisions taken on 2026-09-15 for the desktop
application. It does not restate the architecture already written down in
[CLIENT_CORE.md](../../CLIENT_CORE.md) and
[WINDOWS_CLIENT.md](../../WINDOWS_CLIENT.md); those two remain the
description of what the client is. This document says what was left open
there, and corrects the three places where following them literally would
cost more than it saves.

It also supersedes [MINIMAL_CLIENT_CLI.md](../../MINIMAL_CLIENT_CLI.md),
which describes a client that drives WireGuard and OpenVPN installations the
user made themselves. That is incompatible with an application whose whole
promise is one button.

## 1. Decisions

| Question | Decision | Why |
|---|---|---|
| Repository | Separate repository. Gitea is where work happens; GitHub carries the full source and the releases | Trust in an executable comes from being able to read and rebuild it |
| Language and UI | Go, Wails v2, WebView2 | Same language as the control plane and as the WireGuard tunnel library; the UI stays portable to macOS and Linux |
| Tunnel | Embedded: `wintun.dll` plus the `wireguard-go` tunnel library ship inside the installer. `openvpn.exe` joins them later | A client that requires the user to install two other programs first does not have one button |
| Privilege model | A Windows service from the first working build, never a direct elevated path | See §3 |
| Update | Multi-channel, control plane first, every channel verified against an embedded Ed25519 key | See §6 |
| Code signing | Deferred. The build signs when a certificate exists, and the step is present and disabled until then | No certificate today; nothing about the design has to change when one appears |
| Kill switch | Not in v1 | See §8 |

## 2. Processes

Three binaries, not one.

| Binary | Runs as | Owns |
|---|---|---|
| `svpn-service.exe` | LocalSystem, a Windows service | The tunnel, routes, DNS, every call to the control plane, updates |
| `SingularityVPN.exe` | The logged-in user | The window and the tray icon, and nothing else |
| `svpn-swap.exe` | Launched by the service | Replacing files during an update. No network access |

The user interface and the service speak over the named pipe
`\\.\pipe\SingularityVPN`, with a DACL admitting only the interactive user,
carrying newline-delimited JSON. This is the mechanism
[WINDOWS_CLIENT.md](../../WINDOWS_CLIENT.md) §22 asks for.

**Only the service talks to the control plane.** The interface passes a
username and a password through the pipe once, at sign-in, and never sees the
token that comes back: the service stores it through `SecureStore`, encrypted
with DPAPI at machine scope. Three things follow. The interface cannot leak a
credential it never holds. The tunnel can come up before anybody logs in, so
"connect on start" means the machine, not the session. And there is one HTTP
client in the product, so retries, timeouts and mirror selection are written
once.

## 3. The service is not a later phase

[CLIENT_CORE.md](../../CLIENT_CORE.md) §53 places the Windows service among
the later stages, and [WINDOWS_CLIENT.md](../../WINDOWS_CLIENT.md) §20 calls
it a possibility to be allowed for. The abstraction they describe is right
and is kept unchanged: `VPNPlatform` hides whether an operation runs in
process or through the service, and the core never learns which.

What the abstraction does not protect is everything around it. Without the
service the first build runs elevated, which means a UAC prompt on every
launch — so "install, open, press the button" becomes four steps, one of them
frightening. And when the service does arrive it changes the platform
implementation, the scope the token is stored at, the autostart mechanism,
the installer and the update path. Deferring it does not save that work; it
schedules it twice.

The order is therefore: prove the WireGuard integration under a console
harness running elevated (stage 3), then put it behind the service (stage 4),
then build the interface on top of a service that already exists (stage 5).
No direct elevated path is ever written into a shipped binary.

## 4. Modules

The package layout of [CLIENT_CORE.md](../../CLIENT_CORE.md) §40 is adopted
as written, with one correction: `routing/selector.go` does not exist on the
client. Route selection belongs to the server, as
`SMART_ROUTING.md` in the control plane repository has it and as §42 of that same
document requires when it forbids `GUI -> Smart Routing`. What remains on the
client is `recommendation.go`: receiving a decision, not making one.

The seam that matters most is between what can be tested on a laptop and what
cannot.

| Package | Pure | How it is verified |
|---|---|---|
| `api`, `state`, `security` | Yes | Unit tests against a fake control plane |
| `connection`, `recovery`, `diagnostics`, `events` | Yes | Unit tests with a fake `Tunnel`, a fake `Prober` and an injected clock |
| `protocols/wireguard`, `protocols/openvpn`, `platform/windows` | No | Manual verification on a real machine, under administrator |
| `update` | Yes, apart from the swap | Unit tests over a fake feed; the swap is verified by hand |

Everything expensive to test is thin and does as little as possible.
Everything hard to get right — the state machine, the cooldowns, the
anti-flapping window, the retry budget from
[CLIENT_AUTO_SWITCH.md](../../CLIENT_AUTO_SWITCH.md) — is pure, and runs in a
second without a network or a privilege.

## 5. What the control plane still owes the client

Three additions, all small, all in the main repository.

**`POST /api/v1/auth/device`.** Tokens issued today live for an hour. An
application cannot ask for a password every hour, so it needs a long-lived,
individually revocable device token. This fits the shape the service is
already converging on: the device becomes one more entry in the account's
list of connections, and revoking it is the same act as revoking any other.

**`GET /api/v1/vpn/recommend`.** The MVP flow asks the control plane which
node and protocol to use, and nothing currently answers that without side
effects. `recommendProtocol` is unexported; `/vpn/auto` recommends *and*
provisions; `/protocol-reach` needs a `node_id`, which is half the answer
being asked for. The new endpoint reuses `recommendProtocol` and returns a
node and a protocol, provisioning nothing.

**`GET /api/v1/app/update`.** Serves the signed update manifest described in
§6.

There is also a rule the client must obey rather than an endpoint it needs.
**A profile is created once and then reused.** Calling `/vpn/auto` on every
connection would provision a new client each time and exhaust the account's
`client_limit` within days. The client creates one profile, stores its
`client_id` in local state, and afterwards fetches
`GET /vpn/config?client_id=...&protocol=...` for each connection.

## 6. Updates

The update component is independent of the connection logic, as
[WINDOWS_CLIENT.md](../../WINDOWS_CLIENT.md) §34 requires. Its *transport* is
not: the control plane is the first place it looks. That server is the one
address the application must be able to reach in order to be useful at all,
so excluding it from the update path would discard the most reliable channel
at exactly the moment the others are blocked.

The manifest is a small JSON document — version, release time, artifact
hashes and sizes, a mirror list, a minimum supported version — carried with a
detached Ed25519 signature. The public key is compiled into the binary; a
second, backup key is held offline. No mirror is trusted, which is what makes
adding mirrors free.

Channels are tried in order, each with its own timeout:

1. the control plane;
2. the mirrors named by the last manifest that verified — so the mirror list
   is itself signed and itself updatable, and a blocked mirror is replaced by
   installing the next version rather than by reinstalling the application;
3. the same two, retried through the tunnel once it is up — the strongest
   path there is, because a working tunnel means a working internet;
4. GitHub releases, compiled in as the last resort.

Verification is identical on every channel: manifest signature, then artifact
hash, then Authenticode once a certificate exists.

Applying an update is staged. The service downloads and verifies, then
launches `svpn-swap.exe`, which waits for the service to exit, replaces the
files and starts it again. The new service has sixty seconds to report itself
healthy — one pipe handshake and one successful API call — or the swap is
reversed. Exactly one previous version is kept.

One behavioural rule: **an update is never applied while the tunnel is
carrying traffic**, unless the manifest marks it urgent. Otherwise a person
in the middle of a call gets an interruption from us rather than from a
censor.

Without a code-signing certificate, SmartScreen will report an unknown
publisher. The README says so plainly, and publishes artifact hashes and
instructions for reproducing the build.

## 7. Repository boundary

The control plane address is never committed. It is supplied at build time
from `local/app.env` through
`-ldflags -X singularity-vpn-app/internal/buildinfo.Control_plane=...`. The value checked into the
source is an address from the RFC 5737 documentation range, so a binary built
from a clean clone connects nowhere. That satisfies the boundary rule and is
honest towards anyone who forks the project.

## 8. Not in v1

**The kill switch.** Firewall rules that drop everything outside the tunnel
leave a machine with no connectivity at all if the service dies, and the
person is usually not sitting in front of it when that happens. It arrives as
an opt-in setting with a watchdog that removes the rules when the service
stops, and becomes a default only after the service has run for a month
without dying.

Also absent: per-application split tunnelling, manual choice of a specific
node, Xray and VLESS (which need server-side support that does not exist
yet), and any platform other than Windows.

## 9. Stages

| Stage | Contents | Privilege |
|---|---|---|
| 0 | Repository, CI, `buildinfo`, package skeleton; this design and the two client documents moved in | — |
| 1 | `api`, `state`, `security`, against a fake control plane | — |
| 1b | Server side: `auth/device`, `vpn/recommend`, `app/update` | — |
| 2 | `connection`, `recovery`, `diagnostics` — the whole state machine, on fakes | — |
| 3 | `protocols/wireguard` and `platform/windows`, driven by a console harness | Administrator |
| 4 | The service and the named-pipe protocol | Administrator |
| 5 | The Wails interface and the tray | — |
| 6 | `update`, `svpn-swap`, release signing | — |
| 7 | `protocols/openvpn` | Administrator |
| 8 | Installer, autostart, first run | — |

Stages 1, 1b, 2, 5 and 6 are closed by a subagent with tests. Stages 3, 4 and
7 need verification on a real machine. Stages 2, 4 and 6 are reviewed.

## 10. Done, for v1

A person who has never seen the application installs it, is asked for a
username and a password once, presses one button, and is connected. They
close the window and it keeps running in the tray. They reboot and it
reconnects without being asked. When a protocol stops working, it moves to
another one without telling them. When a new version exists, it installs
itself, and blocking any single source does not prevent that.
