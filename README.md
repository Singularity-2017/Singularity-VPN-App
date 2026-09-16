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

## Building

```
go build ./...
go test ./...
scripts/build.ps1
```

`go build` produces a binary that connects nowhere, on purpose. `build.ps1`
supplies the real values at link time.

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
