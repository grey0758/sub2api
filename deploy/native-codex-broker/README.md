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
pinned verified host key and strict checking. It serves only the application UID1000 through
mode0660 root:1000 `/run/sub2api-native-codex/token.sock` inside a mode0750 root:1000 directory.
The systemd service has no token cache and persists no lease contents. Bind
only the runtime directory read-only into the Sub2API container. Keep its key
and `/etc/sub2api-native-codex` outside the container.

The application process uses:

```
OPENAI_NATIVE_TOKEN_BROKER_SOCKET=/run/sub2api-native-codex/token.sock
OPENAI_NATIVE_TOKEN_BROKER_UID=0
```

The consumer checks server UID0, account/user/expiry and token claim consistency
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

The production entrypoint drops the app to UID/GID1000 even when Docker Config.User
is empty. Preserve that identity; the key-reading broker stays UID0. RuntimeDirectory
is retained across broker restarts so existing container mounts keep the same inode.

### Native HTTP identity and packet acceptance

The HTTP forwarding path negotiates `include_client_identity=true` with the
socket broker. Official pinned Codex `initialize` supplies `user_agent` alongside
the access-only lease, using the CLI client name and the binary's actual version.
The broker retains its five-field response for older consumers. Deploy the
backward-compatible socket bridge before the native RPC reader and application.
A forwarding request fails closed if its authenticated native identity is absent
or invalid. Global latest-version settings do not relabel this account.

Native HTTP preserves supplied Responses Lite instructions/tool declarations in
`input`, without adding synthetic top-level instructions. Full-history tool call
IDs remain intact. The request forwards native hyphenated session/thread/request
headers, and scopes all native UUID aliases consistently per account and API key.
It omits synthetic version/conversation/routing headers and disables automatic
compression in an independent HTTP/1.1 pool. Other accounts retain their existing
identity, namespace and transport policies. Generic API input does not become a
full native Codex prompt unless the caller supplies that content.

Optional synthetic-only production audit: `OPENAI_NATIVE_PACKET_AUDIT_DIR` names
an existing private0700 application-owned directory. A short-lived `control.json`
selects `account_id`, a random `marker` of at least32 characters present in the
synthetic body, and `expires_at` (at most30minutes ahead). At most16 attempts are
recorded; actual transport-written headers and TLS response metadata accompany
the serialized body. Authentication headers are hashed. Remove the control file
after acceptance. Leave the environment unset for no audit. Never capture or
replay customer requests. Go/Rust TLS and header-order equality are not claimed;
a TLS-terminating observer owns the final upstream TLS during isolated tests.


### Capacity and dependent service changes

The HTTP bridge admits ten in-flight model requests. The access broker admits
ten waiting leases, while only two SSH authentication reads run at once;
`forced_lease.py` keeps its native auth lock. This avoids bursts against the
SSH server's MaxStartups without caching access tokens or changing SSH policy.
Both UNIX listeners use a backlog of32. Lease queueing and SSH execution stay
within the application's30second deadline. These are local limits, not a
guarantee of ten simultaneous provider generations or freedom from overload.

The deployed HTTP egress unit requires the token broker unit. Restarting the
token broker restarts dependent egress and interrupts active SSE requests.
Drain and check **both** cgroups before restarting either dependency chain;
checking only active lease children is insufficient. Retain the runtime
directory inode and root:1000/0660 socket policy. Never restart the native
owner's existing Codex services as part of a consumer capacity update.
