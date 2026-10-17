# Private Agent context engineering

Teamie keeps one global Session per Agent and one serial native TurnLoop writer. Context reduction, summaries, archive reads and background completion inputs are Runtime internals. IM has no Eino dependency, and browsers cannot inspect this private context.

## Versioned execution policy

Runtime schema 3 adds immutable `context_profile` and `context_budget` to each logical Run. Existing rows use `teamie-context-v037` and retain the original tools, prompts, summaries and model-receipt hashes. Explicit `agents.context.v1=true` admits new Runs with `teamie-context-v038`; disabling admission does not change an already admitted Run or erase durable data. A saved budget includes the declared model window, input/output reserves, transport ceiling, counter version, reduction thresholds and recent-tail bound. Recovery loads that snapshot rather than recomputing from current gateway settings. Native checkpoints bind both profile and budget hash.

This is a policy version inside the existing runtime, not another execution service. Eino remains `v0.10.0-alpha.35` with the official `agenticopenai v0.2.3` gateway adapter. Habitat's newer local `origin/dev` was inspected at `34ed3daf1f430b238d4f5cc33fb91166b15deb42`; it uses Eino `v0.9.13` and adapter `v0.2.2`. Its native reduction, source-aware prefix compaction and transcript retrieval informed the work. Its per-conversation orchestration and tool catalog are not adopted into Teamie's continuous Session.

## Model input pipeline

The new pipeline reduces large tool observations before summarization. It retains the platform policy, summarizes an older prefix, and keeps a bounded exact suffix of complete recent request/assistant/tool groups. Call and result pairs are not split. Original source requests remain represented even after long tool sequences. Capacity checks account for serialized messages, tools, summary input/output and the independent gateway byte ceiling; shrinking message count alone cannot prove a request fits.

Large observations and compacted transcript projections receive immutable private archive references. `context_read` retrieves UTF-8 byte pages; `context_search` finds relevant observations in the fixed Session view. These tools revalidate the Agent, worker lease and current ordinary IM access to their real source references, including on cached reads. Replayed reads retain their original observation rather than discovering artifacts from later Runs. Retrieval pages are not recursively offloaded, and a summary of them inherits the original server-recorded source references. Reasoning blocks, signatures, provider metadata and service/provider credential fields are excluded from transcript projection. This does not redact secrets a participant puts into ordinary message text. The archive lives in Runtime's database and is not mounted into Sandbox execution children.

Provider context failures use a small sanitized error allowlist. General authentication, rate-limit and malformed-request errors do not trigger compaction. DashScope's documented input-range error is recognized only with its corresponding structured invalid-parameter code ([official error reference](https://www.alibabacloud.com/help/en/model-studio/error-code)). A pre-output context rejection is durably recorded with its request hash, mode, policy and decision before native recovery. A successful receipt and a context rejection cannot occupy the same model step. Partial streaming failures remain distinct and cannot be silently converted into a successful retried response. Native retry observations do not themselves fail a Run that subsequently succeeds.

## Progressive workspace and task observations

New-policy Runs advertise private `workspace_read_page` for file, directory, glob, grep and task-output pages. Responses state coverage, continuation and version so a model can distinguish a preview from a complete result. UTF-8 byte pages can traverse long single lines and files larger than the legacy 256 KiB text-read ceiling. Mutations keep their existing bounds and isolated workspace paths.

Command output is captured in trusted Sandbox state independently of the small model preview. Durable output has explicit physical storage limits and completeness metadata; crossing the preview length does not terminate a new-policy command. Legacy tasks retain their receipt format and replay semantics. An unproven gap in recovered daemon logs is incomplete evidence, never fabricated complete output or permission to rerun an unknown effect.

Background tasks retain their parent Run and actual source reference set in private snapshots. Completion inputs recover that origin from Runtime's own task/Run rows. They do not guess an audience from the newest active conversation. Multiple sources and incomplete bounded reference sets remain explicit; all IM sends still require an explicit destination and current IM authorization.

## Verification and rollout

Meaningful verification covers native reduction and prefix/tail behavior; real PostgreSQL archive visibility, write-once identity, source ACL revocation and restart replay; model-recovery tape ordering; long file and output pages; task origin; and legacy Run compatibility. Provider protocol fixtures establish these mechanics, not real-model decision quality. No new browser diagnostic UI or deployment environment is introduced.

The release schema vector is Identity 3, Chat 3, Control 1, Runtime 3, Gateway 2. Stop the old Runtime writer before migration and dependency replacement, verify the pre-upgrade backup, and deploy immutable ACR digests. Schema 3 prevents image-only rollback to schema 2. Preserve all previous release branches/tags and saved administrator gateway configuration.
