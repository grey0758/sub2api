# Native Codex access-only broker for HTTP Responses

Sub2API's opt-in `extra.openai_native_token_broker=true` account has identity
credentials, never access/refresh/id tokens. `chatgpt_account_id` and optionally
`chatgpt_user_id` must match the native owner. It is Linux-only.

This mode always selects HTTP Responses/SSE. Configure account WS mode `off`
and `openai_ws_force_http=true` as an additional operational guard. HTTP OAuth
normalization retains full tool history instead of relying on WS parent cache.
Use complete input/output/function_call_output history for continuation.

On the native host, `forced_lease.py` is a root-owned, read-only Python SSH
forced command that runs as the existing Codex owner. Its key is restricted
with `restrict,from="<consumer private IP>",command="<fixed command>"`.
It accepts only the pinned account identity and invokes the fixed official
Codex app-server `getAuthStatus(includeToken=true,refreshToken=false)`.
The native managed-auth lifecycle owns OAuth and automatic refresh. This code
does not call OAuth or read/share refresh tokens. A near-expiry lease fails
closed if the official lifecycle cannot provide a current access token.

On the consumer host, `socket_bridge.py` uses this dedicated SSH key with a
pinned verified host key and strict checking. It serves only local UID0 through
mode0600 `/run/sub2api-native-codex/token.sock` inside a mode0700 directory.
The systemd service has no token cache and persists no lease contents. Bind
only the runtime directory read-only into the Sub2API container. Keep its key
and `/etc/sub2api-native-codex` outside the container.

The application process uses:

```
OPENAI_NATIVE_TOKEN_BROKER_SOCKET=/run/sub2api-native-codex/token.sock
OPENAI_NATIVE_TOKEN_BROKER_UID=0
```

The consumer checks server UID, account/user/expiry and token claim consistency
before use. It bypasses Redis token cache and all independent refresh paths,
rejects local token credentials and credential shadows, and never falls back.
No Authorization headers or leases should be logged. OAuth access tokens keep
their official expiry; this interface does not mint shorter-lived OAuth tokens.

Production layout uses `/opt/sub2api-native-codex` for root-owned source,
`/etc/sub2api-native-codex/binding.json` for private identity/host configuration,
and a dedicated consumer SSH key with pinned `known_hosts`. Do not install an
unrestricted key or expose this interface on a public HTTP port. Run
`python3 -B -m unittest discover -s deploy/native-codex-broker -p test_broker.py`
and the Go native broker/HTTP/token provider/refresher tests before release.

UI account connection tests obtain tokens through the same broker. Background
and manual Sub2API OAuth refreshes remain disabled for these accounts. Other
providers and ordinary OAuth accounts retain their existing behavior.
