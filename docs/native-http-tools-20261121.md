# Native Codex HTTP goal context and Chat ingress

Production Pro uses `0.2.20-opencodex.native-http.5`, backend revision
`fd5ef549257434954c4b68e61181b02923b74349`, image `b8b14337451d`.
Canonical traffic switched back at 2026-11-21 21:24:37 UTC. The official native
client remains 0.171.0 with its existing read-only auth binding and HTTP egress.

Codex 0.171 sends synthetic caller-owned goals as `function_call_output` with
namespace `agent` and name `goal_objective`, without a function invocation or
call ID. Two fresh native HTTP baselines accepted both the raw shape and the
shape with its own item ID and internal metadata. Correlation validation now
excludes only this recognized context in a temporary validation projection.
The forwarded body keeps its original item, namespace, metadata and ID; no
call ID is fabricated from the item ID. Ordinary uncorrelated outputs and
ambiguous payloads retain the existing validation rules.

Chat Completions converted to Responses previously obtained an access-only
lease without negotiating native client identity, then failed locally while
constructing the HTTP request. The conversion now creates the same per-request
identity context before requesting its lease, retains the native HTTP profile,
and avoids reintroducing a legacy session header. Login/refresh ownership,
account selection and token storage rules are unchanged.

The final focused regression passed 150 tests/subtests and both packages with
Go 1.27.3. A native-forward test checks exact input bytes, including an annotated
goal. Existing ordinary OAuth metadata cleaning is covered separately. The
binary build passed and all 181 embedded frontend asset hashes are unchanged.

Two fresh `gpt-6.2-sol/xhigh` candidate checks completed with exact synthetic
markers, usage 362184–362185. Four fresh public `gpt-6.1-sol/xhigh` checks through
NewAPI 3001/3002 covered Responses/Chat, JSON/SSE and full tool history. They
uniquely joined channel83 to account256/key3/HTTP, usage362186–362189. The existing
public test tokens allow only6.1, so an initial6.2 negative was rejected403
before dispatch; its failure receipt remains. Another ordinary uncorrelated
output correctly failed Pro400, while NewAPI's existing generic error display
returned503 with quota wording. That text is not official capacity evidence.

The original30 missing-call-ID requests were30 distinct requests represented by
60 middle/final log rows. Their complete original ingress packets were not
retained, so no all-packet attribution claim is made. Two historical Chat502s
were confirmed as local identity construction failures, before upstream
dispatch. No official capacity fault was established or modified in this scope.

The account remains sole native source256 with concurrency10, HTTP forced and
WS off. Broker/native lifetimes, auth/config/binding hashes and other stacks
passed protection checks. Full policy equality is false from natural usage/reset
fields; protected policy equality passed without restoring stale state. The
green container was removed and the previous rollback container remains stopped.

Curator action `b5a193cc-49b5-4aa1-ab1b-9e1806fc1353`. All dated build, release,
native baseline and acceptance intents are consumed. Never replay them, clear
their guards or replay customer requests. Raw bodies and private diagnostics
remain on their owning trusted server; durable receipts contain counts/hashes.
