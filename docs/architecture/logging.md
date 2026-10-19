# Application logs and correlation

Teamie uses native Zap with Elastic's official `ecszap` v1.0.3 encoder. The former
scattered slog/stdlib calls are replaced at the service entry points and error
boundaries. HTTP, Kitex, Runtime, Gateway and Sandbox use the same infrastructure
in `backend/pkg/observability`, which has no Agent, IM or Eino dependency.

This is application logging, without an additional log storage or collection
service. Existing Docker `json-file` rotation remains 10 MiB x 3 files per
container. Logs are internal operator data, never a browser/owner API. Existing
Session/model/tool persistence remains the execution ledger; logs do not replace
receipts or prove an IM send/file delivery occurred.

## Format and privacy

Each application event is one ECS JSON line on stderr. Static bootstrap fields include
`service.name`, `service.version`, `labels.commit_sha`, `service.environment`
(`production`), `service.node.name` and `event.dataset`. The official encoder owns
the timestamp, level, logger, caller and error object. A native policy hook adds
the original error/cause types and fingerprints. Error stack traces come from
native Go stack errors and identify the reporting boundary.

Business calls supply stable messages and allowlisted scalar metadata. Common
fields include `event.action`, `event.outcome`, integer nanosecond
`event.duration`, opaque user/Agent/Run/Attempt/tool/task IDs, numeric capacity,
token/byte/fragment counts, observed dependency attempt counts, state, safe error code and diagnostic stage/reason.
No complete bodies, model text, reasoning, prompts, tool arguments, commands,
file contents/paths, provider URLs, emails, credentials, reset/invitation tokens,
headers, environment dumps or configuration objects are logged.

Native causes remain attached to private errors via `Unwrap` without changing
their public `Error()` or safe error code. Their text is **not** automatically
safe: SDK, SQL, filesystem and HTTP errors can echo private input. Only owned
diagnostic codes are emitted as `error.message`; unrecognized cause text is
fingerprinted. Owned public error descriptions may accompany their codes. Native concat failures are classified as identity/type/extension
conflicts, and stream shape is logged without its contents. This is a diagnosis
trail, not a raw-response capture facility.

The policy hook redacts credential/body/configuration fields and arbitrary
reflected/object values. It is additional protection, not permission to pass
unrestricted data to a logger. Framework diagnostic arguments are represented
by fingerprints and source locations, since they may contain full RPC messages.

## Context lifecycle

Each service initializes one OpenTelemetry SDK context owner, using the standard
W3C Trace Context propagator. No collector or exporter is required or enabled.
The default sampler is parent-based with new roots unsampled; valid trace/span
contexts are still created. Log enrichment reads context when each record is
made and does not depend on `IsRecording` or sampling.

Framework recovery behavior is preserved: the HTTP wrapper logs a panic with
private details removed, then rethrows it to the existing recovery boundary.

HTTP ingress extracts `traceparent`/`tracestate` and starts a local server span.
Missing/invalid parents create new roots. HTTP clients (including attachment download and Sandbox Docker API calls) create a new client span
and inject that operation's context; stream spans end at body EOF or Close,
not at response headers. Each observed provider HTTP attempt is numbered; the
Gateway call duration includes all attempts and any SDK retry/backoff. No new
retry policy is introduced. Kitex clients use native TTHeader transient metainfo
with `TTHeaderFramed`, and server middleware creates a new receiving span.
Generated Thrift contracts are unchanged.
An optional `X-Teamie-Test-Run-ID` is the only extra HTTP test metadata carrier:
1–64 ASCII letters/digits/dot/underscore/hyphen, enriched as
`labels.test_run_id` and propagated through HTTP and internal RPC. Other headers
and baggage are not copied. The label groups independent traces; it is never
authentication or a trace ID. Only allowlisted opaque operation
identifiers accompany the trace in internal RPC metadata.

Run execution gets its own operation scope; the context follows the serial
TurnLoop, model-stream goroutine, tools and downstream calls. Model generation
does not gain a deadline. Closing/canceling a stream logs the actual result.
No tracing or logging code writes a Session or modifies input/canonical request
hashes, checkpoints, receipts, leases or native preemption behavior.

Durable IM inputs and background work are separate causal boundaries. They are
not forced into an unrelated poll/request trace. Input-persisted events expose
only source event/message/conversation IDs. Run-start records include a bounded
input-ID list; task notifications and independent Sandbox supervisor traces
include the originating Run/task IDs. These IDs join the traces across restarts.
Trace context is not inserted into durable input JSON or rewritten receipts.

## Events and measurement scopes

| Event | Scope and useful fields |
| --- | --- |
| `service-initializing`, `service-stopped` | Process lifecycle, version and node; initialization is not a readiness claim. |
| `http-request` | Handler plus response streaming; `custom.http.route` template, method, status, bytes and authenticated actor ID. No raw URL/query. |
| `http-dependency` | One request through response body consumption; headers latency is a separate numeric field. EOF and early consumer Close are distinguished; an early Close alone has unknown outcome. |
| `rpc-client`, `rpc-server` | One invocation, method/service and safe business failure code; client and server have distinct spans. |
| `agent-input-persisted`, `task-notification-persisted` | Only emitted after durable insertion; source IDs join ingestion to execution. |
| `agent-run-started`, `agent-run` | One execution Attempt including foreground-slot wait; Run/Attempt/Session IDs, input count, state, preemption and actual failure code. A queued preemption cannot hide a fatal failure in the log. |
| `model-call-started`, `model-call` | Model-slot wait through receipt replay/save. Step, private failure stage/reason, stream shape, done-frame and receipt-save flags. |
| `model-provider-call-started`, `model-provider-call` | Gateway admission through provider consumption and usage persistence; token counts and terminal failure. Gateway success does not imply Runtime validation/receipt success. |
| `agent-tool-started`, `agent-tool` | Explicit IM/native/history tool through its receipt boundary; name, call ID, result and replay indicator where available. No arguments/results. |
| `sandbox-workspace`, `sandbox-execution`, `sandbox-task` | Workspace operation, foreground execution or independent task supervision; state/result and output length, never command/file/output content. Observer cancellation does not claim that a durable child terminated. |
| `context-compaction-started`, `context-compaction` | Actual reduction attempt, before/after message counts, frozen input limit and forced/proactive selection; never summary content. |
| `agent-reconciliation`, `outbox-cleanup`, `outbox-publish` | Recoverable failures, with safe type/cause metadata. |

Known successful sync/typing, Docker polling, authorization-validation and runtime poll RPCs use debug
verbosity. Their failures remain visible. Successful health-path access logs
are suppressed only after outcome is known and elapsed time is below one second;
failures, exceptions and slow checks remain. This also covers normal
`clb-healthcheck[/version]` and `kube-probe[/version]` traffic without trusting a
spoofable User-Agent to hide other routes. These are log filters, not execution
time limits. There are no per-token/fragment success logs.

## Operator lookup

`LOG_LEVEL=info` is the default production setting. `debug` additionally enables
healthy polling diagnostics. It is a process setting; changing it requires the
ordinary deployment/restart procedure. Existing gateway/provider secrets and
feature flags are unaffected.

On the host, inspect the bounded container logs, then join across services:

```sh
python3 ops/logs.py --service agent-runtime --since 30m --run RUN_UUID --step 1
python3 ops/logs.py --since 30m --trace TRACE_ID
python3 ops/logs.py --service sandbox --since 2h --task TASK_ID
```

The reader is read-only, calls Docker without shell interpolation, parses each
JSON line and matches exact identifiers. An event lookup matches both ingestion's
scalar event ID and the bounded input-ID list on Run-start records, enabling
event-to-Run correlation. It never loads production environment
files or databases. Query a shorter interval if Docker's existing rotation has
already removed older events. Long-term/off-host log retention is not provided
by this change.

For `invalid_upstream_response`, locate `model-call` by Run/Attempt/step and check
`custom.model.failure_stage`, `custom.model.error_stage`,
`custom.model.failure_reason`, cause type/fingerprint, chunk/byte counts,
`done_received` and `receipt_saved`. Join its trace to the Gateway provider
event. If the Run says `interrupted`, inspect its separate `preempted` and
`failure_code` fields. Use `--event EVENT_ID` to join a Run's input list to its ingestion
trace, and `--attempt ATTEMPT_UUID` to isolate retries. Failed responses from older releases cannot be recovered
retroactively by installing this logging.

## Verification boundaries

Local tests capture actual official-encoder output and exercise HTTP ingress,
HTTP clients, real Kitex TCP/TTHeader, asynchronous work, unsampled/no-export
contexts, concurrent request isolation, invalid/missing parents and invalid
tracestate. They verify body-consumption timing, cancellation, WebSocket hijack,
probe failures, exactly one owning completion event and private-data exclusion.
Runtime tests use disposable PostgreSQL and synthetic NDJSON faults to verify
decode/aggregation/validation/interruption/receipt persistence diagnostics.
These fixtures do not claim real-provider or deployed end-to-end acceptance.
Dependency initialization warnings emitted before `main` are outside the
application logger; the operator reader skips non-JSON legacy/dependency lines.
Edge/Nginx and database/Redis native logs retain their existing formats.

Deployment acceptance separately requires checked image digests, versioned
services and one isolated real-Agent request whose internal logs can be joined
by Run/Attempt/step and trace. No production rollout is implied by local tests.

References: [official Elastic ECS Zap setup](https://www.elastic.co/docs/reference/ecs/logging/go-zap/setup),
[OpenTelemetry propagation](https://opentelemetry.io/docs/languages/go/propagation/),
[Kitex metadata](https://www.cloudwego.io/docs/kitex/tutorials/advanced-feature/metainfo/).
