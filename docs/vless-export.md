# VLESS profile export

[Русская версия](vless-export.ru.md)

Open Account → Profile → VLESS beside the existing profile. Choose an available
server's **Create and download VLESS** action and a destination in the native
save dialog. The text file contains one VLESS URI for import into a compatible
client. Cancelling the dialog issues no export request.

This generates an alternative configuration for an existing materialised
profile. It creates no account slot and does not select a profile, start a
tunnel, or add a VLESS adapter to the desktop app.

The service uses its authenticated device token for:

- `GET /api/v1/vpn/vless/options?client_id=...` returning
  `data.nodes[{node_id,vless_available,reason}]`.
- `GET /api/v1/vpn/config?client_id=...&node_id=...&protocol=vless&mode=vpn`
  returning `data{config,protocol,mode,expires_at}`.

Only explicit boolean `vless_available: true` permits download. Capabilities
are checked again before issuance; unavailable nodes, missing fields, errors,
and older servers fail closed. The backend enforces ownership, active status,
materialisation and helper readiness. The desktop validates the returned
protocol, mode and single URI. Credentials travel through the authenticated
service pipe to the native writer, never through the WebView or its DOM.

Verification uses synthetic HTTP, service and browser fixtures; no production
profile is generated and no live tunnel is changed.
