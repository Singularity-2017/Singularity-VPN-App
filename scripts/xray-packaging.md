[Русская версия](xray-packaging.ru.md)

# Offline Xray packaging

Build from previously verified official Windows x64 Xray 26.10.31 files:

```powershell
./scripts/build.ps1 -Setup -XraySource C:/verified/xray-26.10.31
```

Without `-XraySource`, the source is `local/xray`. Builds that include the
service or window require these files even without `-Setup`. No network fetch
or PATH lookup is used. The committed `internal/xraybundle/pins.json` pins
the official archive digest and each copied file. Staging rejects missing or
changed files before replacing the destination; it copies only the allowlist.

The runtime contract is `<service executable directory>/xray/xray.exe`, with
`wintun.dll`, `geoip.dat` and `geosite.dat` alongside it. The upstream LICENSE,
LICENSE-Wintun and README.md are copied verbatim. NOTICE.txt provides the exact
upstream source link; manifest.json records provenance. Do not sign or modify
the official binaries: that invalidates their hashes. The service owns process
lifetime and must release Xray and its DLL before reporting itself stopped.

The setup embeds its own trusted pin and verifies Xray before stopping the
service. A replaced disk manifest cannot authorize new binaries. Installation
uses the existing protected Program Files directory and atomic per-file writes.
Upgrades replace all required files and remove obsolete dependency files;
uninstall removes the complete xray directory. This does not make the entire
multi-file upgrade transactional; existing service restart recovery still applies.

To refresh the dependency, first verify an official archive through the approved
artifact workflow, compare its extracted files, review licensing, then explicitly
update the committed pin. Never derive trusted pins from an arbitrary source
directory during a build. No pilot configuration or credentials belong in a bundle.

The native full tunnel resolves through proxied DoH with `UseIPv4`. IPv6 is
captured by the TUN and routed to a blackhole; it is not forwarded to the
physical interface. This avoids locally accepted IPv6 TCP connections that
cannot complete through a node without IPv6 egress. Health checks require a
trusted TLS response on a socket pinned to this TUN, not just local TCP
acceptance. Ordinary external exports retain their client-specific routing.