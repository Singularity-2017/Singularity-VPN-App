# Orchestration rules

This is the desktop application. The control plane it talks to is a separate
repository, `Singularity-VPN`, and has its own copy of these rules.

You are running as the orchestrator on a Claude Pro plan. The usage limit is
the scarcest resource in this project — scarcer than wall-clock time and
scarcer than your own carefulness. Treat every turn as spending a budget the
owner cannot top up.

## The cost rule (overrides everything below)

Pick the cheapest path that still gets the task right, and say which path you
picked and why in one line, before doing it. The owner does not want to be
asked how to save tokens — decide yourself, state the decision, proceed.

Order of preference, cheapest first:

1. **graft.** `graft ask/grep/skeleton/callers/map` answers "where is it, what
   calls it, what is in this file" for a few hundred tokens. Always first.
   Never grep, glob or read whole files to rebuild understanding graft gives.
2. **Do it yourself.** A mechanical edit — a constant, a string, a default, a
   field rename, a one-file change you can already name — is cheaper done
   directly than specified, delegated, and reviewed.
3. **Delegate.** Use a subagent when the work needs many file reads, produces
   verbose output you must not carry (test runs, log digs), or genuinely
   benefits from a second pair of eyes.

Use `test-runner` for the suite, `implementer` for a change across three or
more files, `reviewer` scaled to risk — the update path, the service, the
credential store and anything running elevated get a review; a fifteen-line
mechanical diff with tests passing does not.

## Review discipline

Subagents are cheaper models and they are wrong often enough to matter. Do not
take a subagent result at face value: check the claim against the code before
reporting it as done.

## Environment facts (do not rediscover these)

- `go test -race` needs a C compiler that targets plain Windows; Cygwin's
  own gcc does not. Run it as
  `CC=/c/cygwin64/bin/x86_64-w64-mingw32-gcc.exe CGO_ENABLED=1 go test -race ./...`
  (Cygwin package mingw64-x86_64-gcc-core).
- Cyrillic through a bash heredoc arrives corrupted. Russian text goes through
  the Write/Edit tools or Python, never a heredoc.
- Shell state does not persist between Bash calls. Use absolute paths.
- `sum.golang.org` does not resolve here -- a DNS filter answers for it and a
  direct fetch hangs. A new module needs
  `GOPROXY=https://proxy.golang.org go get ...`, without the `,direct` the
  default carries; the proxy mirrors the checksum database, so this still
  verifies the module. Never `GOSUMDB=off`. Builds after `go.sum` has the
  hashes are unaffected.
- PowerShell here-strings carry CRLF.
- The agent session may run elevated: it inherits whatever token the
  desktop app was started with, so check rather than assume. Being able to
  is not permission to. The service, the adapter and the routes change the
  owner's networking — hand those to the owner with the exact command and
  what a correct result looks like. What elevation is fine for here: tests
  that only create and inspect objects in a temporary directory or the pipe
  namespace (the pipe owner and data-directory ACL tests), which skip
  themselves when the session is not elevated.
- Nothing about who owns an object created elevated can be assumed. On a
  stock install an elevated administrator's objects are owned by the user,
  not by Administrators; set the owner explicitly (`O:BA`) wherever an owner
  check depends on it.

## Project style

- snake_case identifiers. Go exported names start with a capital because the
  language requires it: `Control_plane`, not `ControlPlane`.
- English-only in code, comments, commits, variables, log lines and any string
  the program itself prints. Russian UI strings are the established exception.
- Prose documentation is bilingual. English is the source of truth and lives in
  the plain name (`README.md`); the Russian translation lives beside it under
  the `.ru.md` suffix. Documents not yet converted stay Russian in the plain
  name until they are. Both carry a link to the other at the top. Change the
  English first, then the Russian in the same commit — a translation that lags
  is worse than none, because it is trusted.
- Russian only in spoken dialogue with the user.
- Verbatim content — never paraphrase, abbreviate, or translate author wording
  or formatting.
- Line endings are LF, enforced by `.gitattributes`.
- The changelog the window shows under Settings -> About lives in
  `cmd/singularityvpn/frontend/about.js`. Every change a person can see or
  feel adds a line to its unreleased entry in the same commit, in plain
  Russian from the user's side of the screen; a release dates that entry
  and opens a new one. The owner relies on it to know what is done.
  What is planned lives in the same file (`plans`): only the nearest version
  carries a number, the rest is "later". A plan that is done moves into the
  unreleased entry. Versions are SemVer: a new capability raises MINOR.
- A number shown in the interface is either a real measurement or it is absent.
  Never a zero, a dash or a placeholder standing for "unknown". A version the
  build did not supply is not printed.

## Repository boundary

The control plane address is never committed. It is supplied at build time from
`local/app.env` through
`-ldflags -X singularity-vpn-app/internal/buildinfo.Control_plane=...`. The
value in the source is an address from the RFC 5737 documentation range, so a
binary built from a clean clone connects nowhere, and
`Test_default_control_plane_is_a_documentation_address` fails if that ever
stops being true.

Nothing describing one specific installation — machine addresses, node state,
verification results, reverse proxy layout — belongs in this repository. Those
go in `local/`, which is git-ignored. The running log of this project lives
in `local/LOG.md`; read it at the start of a session, the chat is not a
reliable record. It holds the stage table, what is open, and what has been
verified on a real machine and when.

The production host address must never be printed to the user or written into a
committed file. Keep it in a shell variable and redact command output.
