# codex005 native HTTP egress

Opt-in Pro configuration uses account256 as the only GPT source:
`OPENAI_NATIVE_ONLY_ACCOUNT_ID=256`, `OPENAI_NATIVE_EGRESS_REQUIRED=true`,
`OPENAI_NATIVE_EGRESS_SOCKET=/run/sub2api-native-codex/http.sock`.
Generation never falls back to a Go/public proxy or another GPT account.

The root UNIX service admits only UID1000 and uses the existing pinned,
source-IP-restricted SSH key/forced command. Only fixed `lease`, `catalog` and
`native-http` commands are accepted. The native owner validates account/user,
expiry, exact native UA and current official model/list membership before
forwarding fixed Responses endpoints to chatgpt.com. No arbitrary destinations,
refresh/id token storage, manual OAuth refresh, retries or redirect following.

The final request leaves us003 with the same Python/OpenSSL HTTPSConnection,
verified TLS context and identity/close policy as the native capture proxy.
It uses real bridge clientInfo us003_outer_tool_relay/0.1 and pinned official
Codex0.163. Backward-compatible lease negotiation retains CLI identity for older
consumers. Content body and tools remain caller-owned; native prompt/session
execution is not synthesized by a header proxy.

The root5minute sync job calls official model/list under codex005 through the
restricted catalog command, validates identity and updates account256's exact
identity mappings through the supported admin API. Catalogue expires after1hour;
an expired native catalogue blocks dispatch. Failed refresh retains previous
policy and does not enable another source. Group7 model discovery is pinned to
account256 with scheduler fallback disabled. Both public models and native
manifest use the intersection of official visible model/list and the native
account backend manifest. This excludes hidden backend entries and obsolete
client entries: the pinned client lists gpt-5.5 but a fresh native request returns
400/model_not_found for this ChatGPT account. The initial November11 acceptance covered seven; use the current native
catalogue for later supported models.
No wildcard aliases or synthetic advertised models are added.

SSE bytes and full-history tool outputs pass through the same bounded transport.
Client cancellation terminates the owned SSH child. A connection or request
failure is surfaced; the transport does not replay unknown submissions. Existing
Sub2API application-level error handling remains separately visible in audits.
Synthetic capture requires a private, expiring random-marker control on us003;
auth values are hashed and raw bodies remain private, with an8attempt cap per fresh scope.
The us003 control is
`~/.local/state/sub2api-native-egress/packet-audit/control.json`; its private
`production` directory must belong to a fresh `sub2api-cli-align-*` scope and the
expiry must be at most30minutes ahead. Without a matching random synthetic marker,
TLS observation is disabled. Remove the control after the bounded acceptance.

Capture actual final header order/TLS/source address and passive OpenSSL
ClientHello records. `client_hello_profile` removes random/session/key-share bytes
and compares cipher, extension, version, group, signature and ALPN profiles.
Random handshake bytes and per-user scoped IDs remain distinct. Header/profile
and payload equality in a finite isolated HTTP sample does not establish native
outer bridge RPC lifecycle, WS, all-model or actual refresh-rotation equivalence.
Account/API-key isolation remains. Raw synthetic bodies stay on the owner host;
only allowlisted fields, counts, hashes and booleans leave it.
