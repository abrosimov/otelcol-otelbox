---
note_id: "20261031120000"
created: 2026-10-31
language: en
type: note
subtype: agent-artifact
retention: active
note_role: artifact
artifact_kind: proposal
publication_status: draft
status: active
authorship: agent
assistant: Claude Code
source_refs: []
related:
  - "[[PC-01-ack-after-persistent-enqueue]]"
  - "[[PC-10-identity-and-correlation-preservation]]"
  - "[[PC-11-at-least-once-duplicate-windows]]"
  - "[[durable-admission-audit]]"
---
# Proposal — One agent-transcript receiver for Claude Code, Codex and agy

## Objective

Replace three per-engine Langfuse delivery paths on workstations with one
edge-role receiver, `agent_transcript`, that tails coding-agent transcripts,
decodes them with one decoder per engine, and emits OTel spans carrying
Langfuse attributes and deterministic identifiers into the ordinary edge
pipeline:

```text
transcript files -> agent_transcript/<engine> -> memory_limiter -> resource_detection
  -> redaction/secrets -> otlp_grpc/gateway (file_storage WAL)
  -> gateway traces/selected -> span_dedup -> otlp_http/selected_traces (Langfuse)
```

Durability, checkpointing, identifier derivation, batching, drift detection
and failure handling are shared. Engine code only locates and decodes
transcripts and maps them to an engine-neutral observation model.

The proposal covers the seven questions of the task: per-engine sources and
mappings, the decoder interface, identifiers, the failure model, duplicate
windows, publication layout and rollout. Decisions that belong to the owner are
collected in "Risks And Decision Gates".

## Output

### Finding 01: today's three paths, measured

| Engine | Current path | Durability | Identifiers | Size: mapping vs infrastructure |
| --- | --- | --- | --- | --- |
| Claude Code | Stop/SessionStart/SessionEnd hook running the vendored `langfuse/claude-observability-plugin` 1.2.1 with patches 0001–0008, a wrapper, a spool and a 5-minute LaunchAgent replay runner (devbox-setup `dot_claude/bin/`). | Its own: fsync checkpoints, local OTLP spool, transcript snapshots, pending markers, replay. | Deterministic (patch 0008). | Hook 3,720 lines: 2,716 mapping (of which ~340 exist only because of the hook-firing model), 942 infrastructure, 62 tailing. Plus 643 infrastructure lines outside the hook (wrapper 274, replay 137, durable exporter 232). Infrastructure ≈ 1,650 of 4,360 lines (38 %). |
| Codex | Stop hook running `langfuse/codex-observability-plugin` at `46e3936b` (v0.1.0), re-parsing the whole rollout file per firing, sending to the edge `otlp/langfuse_plugins` receiver. | None beyond a `<rollout>.langfuse` sidecar of uploaded turn IDs. | Random span IDs; random trace IDs (no `trace_seed` configured). An incomplete turn re-uploaded on a later `Stop` becomes a new trace. | 1,113 source lines: 480 decoding, 222 mapping, 411 infrastructure (37 %). The pin is 39 commits behind upstream v0.4.3. |
| agy | The Claude hook, symlinked, registered on `Stop` with `enabled: false` since devbox-setup `01003b2`. | — | — | Cannot work: agy sends `conversationId` (not `session_id`) and its rows are `USER_INPUT`/`PLANNER_RESPONSE` with no `message` object, so the hook finds no session and no turns. Since patch 0006 the hook also imports a module absent from the agy vendor directory. |

The receiver removes the whole infrastructure column: the edge already owns
durable admission (PC-01), retry, authentication recovery and the gateway leg.
What remains per engine is the decoding and mapping column.

### Finding 02: native telemetry of Codex and agy is not sufficient

The question was left open for Codex and agy. The evidence closes it.

| Need for a Langfuse trace | Codex native OTel (`codex-rs/otel`) | agy native telemetry |
| --- | --- | --- |
| Model input and output per call | No. Prompt text only in logs, and only with `log_user_prompt`; model output never, apart from the opt-in final answer `codex.agent_response` (64 KiB cap). | No OTLP export at all; telemetry goes to Google Clearcut. Upstream request `google-antigravity/antigravity-cli#366` is open. |
| Tool calls with arguments and results | Arguments and output in `codex.tool_result` logs; spans carry lengths only. | — |
| Token usage on a generation | Only as attributes of the internal `handle_responses` span. | — |
| Deterministic span IDs | No; SDK-random. `task_started.trace_id` exists only while native tracing exports. | — |
| Resource attributes | Not settable (only `span_attributes`). | — |

Verdict: both engines need the transcript path. Codex's native logs and
metrics keep flowing to the edge unchanged; they are enrichment, not trace
content. `OTEL_EXPORTER_OTLP_ENDPOINT` in devbox-setup's `agy.fish` has no
effect.

### Finding 03: transcript sources per engine

#### Claude Code

| Aspect | Fact | Status |
| --- | --- | --- |
| Main transcript | `~/.claude/projects/<cwd with non-alphanumerics as ->/<session-uuid>.jsonl` | documented (code.claude.com, "Application data") |
| Subagents | `<project>/<session>/subagents/agent-<id>.jsonl` with `agent-<id>.meta.json` (`toolUseId`); workflow agents under `subagents/workflows/<runId>/` with `journal.jsonl` | location documented; meta and workflow layout undocumented |
| Set-aside files | `<session>.orphaned-<ts>-<suffix>.jsonl`, `<session>.jsonl.superseded-<ts>` — must be excluded | documented |
| Lifecycle | Append-only per session; resume appends to the same file; rows whose `sessionId` differs from the file stem are copied history and are dropped. Deletion after `cleanupPeriodDays` (devbox: 3650). | append-only inferred from the hook and local survey |
| Row schema | Internal; the hook says so itself. `version` on every chat/system/attachment row (2.1.220–2.1.291 locally; files can mix versions). | undocumented |
| Record types | Decoded: `user`, `assistant`, `queue-operation` (task notifications). Present and ignored: `attachment` (34 subtypes), `system` (incl. `compact_boundary`, `turn_duration`), `cost-state`, `mode`, `permission-mode`, `ai-title`, `pr-link`, `file-history-*`, `agent-name`, `last-prompt`, `atis-latch`; subagent files add `started`, `result`, `launched`, `fork-context-ref`. | observed |

#### Codex

| Aspect | Fact | Status |
| --- | --- | --- |
| Location | `$CODEX_HOME/sessions/YYYY/MM/DD/rollout-<UTC ts>-<thread_id>.jsonl` (`codex-rs/rollout/src/lib.rs:86-87`, `rollout_file_name.rs:62-73`) | source |
| Resume | Same file reopened in append mode; `session_meta` may repeat with the same id (`recorder.rs:1012-1024`) | source + observed |
| Fork / subagent | New file; first `session_meta` carries `forked_from_id`/`parent_thread_id` and the file then copies the parent's records; subagents mark the boundary with `subagent_history_start_ordinal` (`protocol.rs:3485`) | source + observed |
| Revert | New immutable file `rollout-<ts>-<thread_id>_<rollout_id>.jsonl` (`recorder.rs:98-104`) | source |
| Archive | `rename` into flat `archived_sessions/` (`thread-store/src/local/archive_thread.rs:67-120`) | source |
| Off-by-default rewrites | `local_thread_store_compression` (zstd after 7 days) and `background_paginated_rollout_migration` (atomic rewrite) — either breaks a byte-offset tailer | source |
| Schema | Internal; `RolloutLine` deliberately has no `Deserialize`. A JSON Schema is produced by the hidden `codex app-server generate-internal-json-schema`. Upstream comments name "consumers that tail rollout JSONL" (`protocol.rs:3493`). | source |
| Since 0.147 (`history_mode: paginated`, default) | Line-level monotonic `ordinal`; `user_message`, `agent_message`, `sub_agent_activity`, `*_end` events are no longer persisted; execution details arrive as `event_msg/item_completed`. Locally 1,417 paginated vs 186 legacy files. | source + observed |
| Tracing keys | `session_meta.{id, session_id, cli_version, history_mode}`; `task_started`/`task_complete`/`turn_aborted` with `turn_id` (UUID) and `root_turn_id`; `turn_context.model`; `token_usage_record.response_id` per model call (since ~0.153); `function_call`/`custom_tool_call` paired with outputs by `call_id` | source + observed |

The pinned plugin depends on records paginated mode no longer writes, so its
subagent nesting is already broken for current sessions.

#### agy (Antigravity CLI)

| Aspect | Fact | Status |
| --- | --- | --- |
| Lineage | Closed-source Go binary from the Windsurf/Codeium "Cascade" code (`third_party/jetski/...`), not from Gemini CLI | verified from binary strings |
| Version | 1.2.10 installed (self-updated from cask 1.0.16); upstream 1.4.12; all local data predates 1.2.10 | verified |
| Transcript | `~/.gemini/antigravity-cli/brain/<conv-id>/.system_generated/logs/transcript.jsonl` (truncated fields) and `transcript_full.jsonl` (untruncated) | documented in the docs embedded in the binary and passed as `transcriptPath` to hooks |
| Line fields | `step_index`, `type`, `status`, `source`, `created_at` (seconds), optional `content`, `thinking`, `tool_calls[]{name,args}`, `exit_code`, `error`, `truncated_fields` | observed |
| Ordering | Not append-ordered: 31 of 40 files have out-of-order `step_index`; `RUNNING` steps exist; the 1.1.13 changelog says compaction rewrites the transcript | observed + changelog |
| Missing (1.2.10) | Model name, token usage, tool-call IDs, conversation ID inside lines (path only). A third-party report says ≥1.4.2 adds `model_name` and token fields on `source=MODEL` lines — unverified. | observed; unverified claim |
| Elsewhere | `conversations/<conv-id>.db` SQLite with protobuf blobs (`gen_metadata` → `ModelUsageStats`), undocumented | observed |
| Lifecycle | No rotation; deletion only through the `/resume` picker | observed + changelog |

### Finding 04: mapping specification, per engine

The engine-neutral observation tree is the one the Claude hook produces today,
because Claude's identifiers depend on parent links (Finding 06):

```text
trace (one per user turn)
└── root span "<Engine> Turn"              type span, langfuse.internal.as_root=true,
    │                                       parent = phantom "carrier" span id (never exported)
    ├── generation "LLM Call"              one per model response
    ├── tool "Tool: <name>"                sibling of the generation that called it
    ├── span "Subagent: <description>"     sibling of its Tool span; children recurse
    └── span "Workflow agent: ..."         Claude only; parent = the Tool: Workflow span
```

Trace-level attributes on every span: `langfuse.trace.name`, `session.id`,
`langfuse.user.id`, `langfuse.trace.tags` (comma-separated string, as
`transform/langfuse_native` already writes it so the deny-list sees it),
`langfuse.environment`. Per observation: `langfuse.observation.type`,
`.input`, `.output` (JSON strings, `max_chars` 20,000 with truncation
metadata), `.level`, `.status_message`, `.model.name`, `.usage_details`, and
`langfuse.observation.metadata.<key>`. Level `ERROR` also sets OTel status
`ERROR`. The resource carries `service.name=<engine>` and
`otelbox.telemetry.class=llm`, which routes the spans to the selected-traces
recipient.

| Element | Claude Code (parity with hook + 0008) | Codex | agy |
| --- | --- | --- | --- |
| Turn boundary | Non-meta `user` row that is not a tool result, task notification or interrupt marker; closed by the next such row, by `SessionEnd`-equivalent idle, and deferred while an async `Agent`/`Task`/`Workflow` launch awaits its notification | `task_started` … `task_complete`/`turn_aborted` | `USER_INPUT` step … next `USER_INPUT` or all steps terminal and idle |
| Generation | One per `message.id` (split rows merged); model, usage (`input`, `output`, `cache_read_input_tokens`, `input_cache_creation_5m/1h`), thinking, tool calls | One per `token_usage_record` (`response_id`); model from `turn_context`; usage `input`, `cached_input`, `output`, `reasoning_output` | One per `PLANNER_RESPONSE`; model and usage only if present (Gate G4) |
| Tool | `tool_use` ↔ `tool_result` by `tool_use_id` | `function_call`/`custom_tool_call` ↔ output by `call_id`; `item_completed` enriches timing | `tool_calls[i]` of a planner step ↔ the following result steps, by order |
| Subagent | Subagent file read when the parent unit is emitted (as the hook does) | Separate thread file; spans placed into the parent trace via `root_turn_id`; records before `subagent_history_start_ordinal` skipped | Out of scope for the first release (separate conversation, parent link only in an SQLite summary) |
| Timestamps | Row `timestamp`, rounded to microseconds; fallback chain parent start → 1970-01-01; end never wall clock | Line `timestamp`; `item_completed.started_at_ms`/`completed_at_ms` where available | `created_at` (seconds) plus `step_index` microseconds, to keep order deterministic |

Generation input: the hook sends the full ChatML history of the session on
every generation. The receiver proposes the delta (the new user message or the
tool results since the previous step) — see Gate G2.

### Finding 05: the decoder interface

One component type, `agent_transcript`, one instance per engine
(`agent_transcript/claude_code`, `agent_transcript/codex`,
`agent_transcript/agy`), so configuration, metrics and failure isolation are
per engine while the code is shared.

```go
// Engine is everything engine-specific. All methods are pure with respect to
// OTel, identifiers, storage and delivery.
type Engine interface {
	Name() string                             // "claude_code", "codex", "agy"
	Sources(cfg EngineConfig) SourceSpec      // include/exclude globs, read mode
	Identify(path string, head []byte) (SourceKey, error)
	NewDecoder(src SourceInfo, carry []byte) (Decoder, error)
	Classify() RecordTable                    // known-decoded, known-ignored record types
	TestedVersions() VersionRange             // from the fixtures in testdata
}

type Decoder interface {
	// Feed receives one complete record. It never fails the source: an
	// unknown or malformed record returns an Outcome, not an error.
	Feed(rec Record) Outcome
	// Ready returns units whose observations are final, with the byte range
	// or record keys each covers. Idle tells the decoder how long the source
	// has been quiet so it can close a turn.
	Ready(idle time.Duration) []Unit
	// Carry is the small decoder state the checkpoint stores next to the
	// cursor: turn numbers, keys already emitted in the open unit.
	Carry() []byte
	SourceVersion() string                    // from the records, "" if absent
}

type Unit struct {
	Key          string        // e.g. "turn:<uuid>"
	Observations []Observation // engine-neutral; no OTel types
	Cursor       Cursor        // the safe resume point after this unit
}

type Observation struct {
	TraceSeed, SpanKey, ParentSpanKey string // inputs to the shared ID policy
	Type, Name                        string
	Start, End                        time.Time
	Input, Output                     any
	Model                             string
	Usage                             map[string]int64
	Level, StatusMessage              string
	Metadata                          map[string]any
}
```

Two read modes live in the shared core:

- `append`: byte-offset tailing with partial-line hold-back (Claude, Codex).
- `snapshot`: re-read the whole file on change, key records by an engine key
  (`step_index`), emit a unit only when its records are terminal; the cursor
  is the set of emitted unit keys plus a high-water mark (agy, whose transcript
  is rewritten and out of order).

A fourth engine adds: an `Engine` implementation (discovery, identity, record
table, decoder that produces `Observation`s), its ID seeds under a new
namespace, fixture transcripts per tested version, and its harness fixture.
It adds no storage, delivery, ID, batching or drift code. By the measurements
above that is roughly the plugin's decoding and mapping share: 500–800 lines.

### Finding 06: deterministic identifiers and timestamps

One ID policy, parameterised by seeds the engine supplies.

**Claude Code — patch 0008, preserved exactly.**

```text
trace_id   = sha256(utf8("{session_id}:{turn_identity}"))[:16]
             turn_identity = user row uuid, else "turn:{N}" with N from the
             carried turn counter
phantom    = derive_span_id(trace_id, 0, "carrier") or 1
derive_span_id(t, p, key, attempt=0):
             material = "{t:032x}:{p:016x}:{key}" + ("#{attempt}" if attempt)
             return big-endian uint64 of sha256(utf8(material))[:8]
collision  = while id == 0 or id == parent or id in issued(trace): attempt++
root key   = "turn-root:{session_id}:{turn_identity}", parent = phantom
keys       = "gen:{message.id}" | "gen:noid:{index}",
             "tool:{tool_use_id}" | "tool:{gen_key}:{i}",
             "subagent:{tool_use_id}",
             "workflow-agent:{tool_use_id}:{agentId}"
parents    = generation, tool, subagent -> root; workflow agent -> Tool: Workflow;
             subagent children -> their subagent span
times      = transcript timestamp rounded to µs; start fallback parent start,
             then 1970-01-01; end fallback start (zero duration)
```

The Go port is tested against vectors produced by devbox-setup's
`bin/test_langfuse_deterministic_ids.py` and against IDs captured from the
hook for the Claude fixture. Two hook features are not reproduced: attached
mode (`CC_LANGFUSE_TRACEPARENT`) and seed mode (`CC_LANGFUSE_TRACE_SEED`), both
driven by environment variables of the process that fires the hook, which a
tailer cannot see (Gate G3).

The collision suffix depends on emission order in the hook. The receiver
computes `issued` over the whole unit in canonical key order. The two orders
agree except on a genuine 64-bit collision within one trace; this is recorded,
not engineered around.

**Codex and agy — the same construction under engine namespaces.**

| ID | Codex | agy |
| --- | --- | --- |
| trace seed | `codex:{session_meta.session_id}:{root_turn_id}` | `agy:{conv_id}:{step_index of the opening USER_INPUT}` |
| root key | `turn-root:{thread_id}:{turn_id}` | `turn-root:{conv_id}:{step_index}` |
| generation key | `gen:{response_id}`; before `token_usage_record` existed, `gen:{turn_id}:{step}` | `gen:{step_index}` |
| tool key | `tool:{thread_id}:{call_id}` | `tool:{step_index}:{i}` |
| subagent placement | trace from the parent's `root_turn_id`; parent span = the parent turn's root key | — |

Turn indices are never used as seeds for Codex: they shift with inherited
prefixes, rollback and revert. `turn_id` and `call_id` are UUID-like and stable
across resume. The agy seeds rest on `step_index` surviving compaction, which
is unverified (Gate G4).

### Finding 07: durability and failure model

Safety invariant, shared by all engines:

> **S-T**: a transcript record is covered by a committed checkpoint cursor only
> after every observation derived from it has been accepted by
> `ConsumeTraces`, i.e. persisted in the edge WAL (PC-01). Re-emission is
> allowed; skipping is not, except where the source itself is gone.

The cards below follow the thin C.22.2 form of PC-01..PC-14 and continue the
numbering. They become separate notes in `problem-cards/` when the
implementation provides their evidence.

| Card | Signal / entity | Claim | Acceptance probe |
| --- | --- | --- | --- |
| PC-15 Checkpoint after admission | Cursor commit vs `ConsumeTraces` | For each unit: build spans, split into batches under the gateway ingest cap, `ConsumeTraces` each, and only after the last returns nil commit `{cursor, carry}` in one storage `Batch`. A crash between enqueue and commit re-emits the unit (duplicate), never skips it. Upstream filelog advances its offset even on consumer error; that pattern is explicitly not reused. | Harness: SIGKILL after enqueue and before commit (fault hook in the CI overlay), restart, record delivered; Langfuse double sees it once. |
| PC-16 Refusal and back-pressure | `memory_limiter` refusal, blocked queue, gateway down | An error from `ConsumeTraces` leaves the cursor unchanged and retries the same unit with backoff; a blocked enqueue blocks only that source's reader. The producer is never back-pressured: the transcript on disk is the buffer, so the hook's spool and replay runner have no successor. | Harness: gateway down while a transcript grows; restart gateway; all turns delivered. |
| PC-17 Partial last line | A line without `\n` | Never decoded; held back until terminated. An unterminated tail beyond `max_line_bytes` is counted, not decoded. | Unit: write half a line, poll, finish the line. |
| PC-18 Rotation, rename, truncation, deletion | File identity | Identity is the engine `SourceKey` (Claude/Codex: session or thread ID from the head record, plus a fingerprint of the first 1 KiB), not the path. A rename (Codex archive) continues at the stored cursor. Size below cursor or a changed fingerprint is a rewrite: reset decoder, re-read from zero; IDs are deterministic, so the re-emission is a duplicate. Deletion before the end was read loses only what the source no longer has; it is counted. Checkpoint entries of vanished sources are pruned after a grace period. | Harness: rename mid-tail, truncate-and-rewrite, delete. |
| PC-19 Restart and first start | Checkpoint store | Restart resumes from the committed cursor and replays the open unit from its start, skipping keys already emitted (carried). On first start, sources modified within `initial_replay_window` (proposed 48 h, below the 72 h `span_dedup` TTL) are read from the beginning; older sources are decoded silently to their end to build carry state, emitting nothing. | Harness: SIGKILL during tail; restart; no gap. |
| PC-20 Format drift | Unknown or changed records | See Finding 08. Unknown records never stall or drop a file; a decoder panic on one record is recovered, the record is counted and skipped, and the source continues. | Unit: fixture with unknown types and fields; decoder fuzz test. |
| PC-21 Open-unit bounds | Turns that never close | A unit is closed by the next boundary, by an engine terminal record, or after `turn_idle_timeout` (proposed 30 min). Observations that complete inside an open unit are emitted at once (as the hook does) so long turns appear in Langfuse; the root span is emitted on close. Rows arriving after an idle close become late observations in the same trace; the root's output is not updated. Memory per open unit is bounded by `max_open_unit_bytes`; beyond it the unit is force-closed and counted. | Unit: idle close, late row, oversize unit. |
| PC-22 Checkpoint storage failure | `file_storage/agent_transcripts` | A failed commit pauses the source (no further emission) and retries, so the re-emission window cannot grow without bound; it is a metric and a readiness reason. The checkpoint store is separate from the WAL, so the two writes are separate transactions — duplicates, never loss. | Unit with a failing storage client. |
| PC-23 Cross-file dependencies | Claude subagent files, async task notifications | A Claude unit that launched an async agent stays open until the notification row arrives (or the idle bound); subagent files are read when the parent unit is emitted, exactly as the hook reads them, so subagent IDs keep their parent links. Codex subagents need no join: their trace and parent derive from `root_turn_id`. | Fixture with a subagent and an async agent. |
| PC-24 Snapshot sources | agy rewrite and out-of-order steps | In `snapshot` mode a unit is emitted only when all its steps are terminal (`DONE`/error) and the source has been idle for `snapshot_settle`; the cursor is the set of emitted unit keys. A compaction rewrite that keeps `step_index` re-emits nothing; one that renumbers would emit duplicates under new IDs (Gate G4). | Unit: shuffled and rewritten fixture. |
| PC-25 Inherited history | Codex forks/subagents, Claude copied rows | Records before `subagent_history_start_ordinal`, and Claude rows whose `sessionId` differs from the file stem, are skipped. A fork without that marker re-emits inherited turns under the parent's IDs: duplicates that `span_dedup` drops within its TTL. | Fixture with a forked rollout. |
| PC-26 Redaction boundary | Transcript content | Input/output and metadata are span attributes and pass `redaction/secrets`. Span names (`Tool: <name>`, `Subagent: <description>`) are an uncovered carrier; the description is model-written text. Kept for parity with the hook and recorded. | Harness: a secret in tool output is masked at the Langfuse double. |

### Finding 08: catching drift

All three formats are internal and change often (Codex: ~573 commits to
`protocol.rs`, a default history mode that dropped records in 0.147; Claude:
four versions in local files, several mixed; agy: a changelog that is the only
schema log). Drift handling is therefore a shared subsystem with five layers,
none of which ever blocks delivery.

1. **Per-record classification.** Every record gets exactly one outcome:
   `decoded`, `ignored_known`, `unknown_type`, `degraded` (a decoded type with a
   missing or mistyped field the decoder needs), `malformed`. Metric
   `otelcol_receiver_agent_transcript_records_total{engine, outcome, record_type}`;
   record types not in the engine's table are reported as `record_type="other"`
   to bound cardinality, and each new name is logged once per process with
   its source version.
2. **Source version.** The decoder reports the version it sees (Claude row
   `version`; Codex `cli_version` and `history_mode`; agy none — the shape
   fingerprint below stands in). Gauge
   `otelcol_receiver_agent_transcript_source_version_info{engine, version, tested}`
   where `tested` compares against the fixture range compiled into the
   binary. An untested version is a warning, not a stop.
3. **Shape fingerprint.** For each decoded record type, the decoder declares
   the fields it reads. A field that disappears raises `degraded`; a field
   that appears on a decoded type is counted as `new_field` once per name.
   This catches "the same type, a different shape", which a type table misses.
4. **Semantic invariants.** Per-engine checks that fail when the decoder is
   silently producing less than the source contains: an assistant row with
   usage but no generation; a tool call with no result at unit close; a turn
   with records but no observations; agy steps still `RUNNING` after
   `snapshot_settle`; a Codex file whose first record is not `session_meta`
   (e.g. a `.zst` file after compression was enabled). Metric
   `otelcol_receiver_agent_transcript_invariant_violations_total{engine, invariant}`.
5. **Upstream schema watch in CI.** A scheduled workflow, separate from
   publication like `redaction-conventions.yml`, installs the latest Codex
   CLI, runs `codex app-server generate-internal-json-schema`, and diffs the
   rollout schema against a committed snapshot; a diff opens a failing run
   with the changed definitions. Claude and agy publish no schema, so for them
   layers 1–4 on real transcripts are the signal.

The repository supplies the metrics and a recommended alert per layer in the
edge reference note; alert rules live in the deployment. A drift response is a
new fixture from the new version, a decoder change and an extended tested
range — the same change shape every time.

During the Claude transition a sixth, temporary signal exists: hook and
receiver emit the same IDs, so `span_dedup` drop counts on the gateway measure
ID parity directly (Finding 10).

### Finding 09: duplicate windows that remain

| Window | Cause | Covered by |
| --- | --- | --- |
| Crash between enqueue and checkpoint commit | PC-15 | `span_dedup` on the Langfuse leg (identical IDs) |
| Rewrite, truncation, re-read after fingerprint change | PC-18 | `span_dedup` |
| First-start replay window | PC-19 | `span_dedup`, because the window is shorter than its TTL |
| Hook and receiver in parallel (Claude transition) | rollout | `span_dedup`, identical IDs by construction |
| Codex fork without an inheritance marker | PC-25 | `span_dedup` within TTL |
| Gateway WAL resend after a lost recipient ACK | PC-11 | not covered (dedup sits before the queue); recipient's to absorb |
| Any of the above older than the dedup TTL | TTL 72 h | not covered |
| All-signal recipient | `span_dedup` runs only in `traces/selected` | not covered; at-least-once by contract |
| Codex plugin → receiver overlap | plugin IDs are random | not coverable; kept to a bounded overlap (Finding 11) |

`span_dedup` keys on `(trace_id, span_id)` only and compares no content, so
every emission of a given ID must be final. This is why the root span is
emitted once, on close, and why late rows after an idle close add new
observations instead of updating old ones.

### Finding 10: publication layout and versioning

Today `internal/processors/spandedup` declares
`github.com/abrosimov/otelcol-otelbox/processor/spandedup` and the exporters
declare `.../exporter/otlp{,http}authretry`. None of these paths exists in the
repository, no per-module tag exists, and they build only because OCB turns
`path:` into a local `replace`. Nothing outside this build can `go get` them.

Recommendation — agree with the owner's leaning, with one adjustment:

- **Publish** `receiver/agenttranscriptreceiver` and
  `processor/spandedupprocessor`, contrib-style: the directory equals the
  module path, the component name follows contrib's `<name><kind>` convention
  (renaming `spandedup` is free while nobody imports it), each module has
  `metadata.yaml` with per-signal stability, `mdatagen`-generated status and
  component tests, and a README with configuration.
- **Keep unpublished** `otlpauthretry` and `otlphttpauthretry`. They adapt one
  pinned upstream release, read status codes from upstream error text
  (`TestClassifyErrorReadsTheUpstreamMessage`) and carry `UPSTREAM_VERSION`;
  that is not an API to offer. Adjustment: give them module paths that match
  their directories (`.../internal/exporters/otlpauthretry`) so the paths stop
  claiming a location that does not exist. Go's `internal` rule then needs the
  generated module under this repository's path: set `dist.module:
  github.com/abrosimov/otelcol-otelbox/_build` in `builder.yaml`.
- **Versioning in lockstep with Collector core `v0.x`**, as contrib does:
  public modules carry the `otlpreceiver` pin's version, which the
  `manifest resolve` check already enforces. A component change between
  Collector releases takes the next patch (`v0.163.1`). Stability is stated
  per signal in `metadata.yaml`: `agent_transcript` starts at `development`
  for traces; `span_dedup` at `alpha` for traces, since it is in production
  with harness proof.
- **Tags**: path-prefixed per-module tags (`receiver/agenttranscriptreceiver/v0.163.0`),
  cut by the default-branch workflow like `v<dist.version>`, only when the
  module's tree changed since its last tag and the build is green; immutable
  like release tags. Hand-cut tags stay forbidden. The artefact's
  `v<dist.version>` is unchanged and independent.

### Finding 11: rollout order and migration plan

Order: **Claude Code → Codex → agy.**

- Claude first: largest infrastructure to remove, and the only engine with
  reference IDs that prove the port (parity is measurable).
- Codex second: native telemetry is insufficient, the pinned plugin is broken
  for paginated sessions and emits random IDs, and the format has a schema
  generator for the CI watch.
- agy last: no working path exists, so there is nothing to cut over, and the
  transcript content on the current version is unverified (Gate G4).

Per engine, in devbox-setup (separate repository and pull request, later):

| Step | Claude Code | Codex | agy |
| --- | --- | --- | --- |
| 1 | Release the collector with the receiver; render `agent_transcript/claude_code` with `initial_replay_window` 48 h. Hook stays on. | Render `agent_transcript/codex` with `emit_turns_ended_after: <T>` where T is the planned plugin removal time minus a 5-minute overlap. | Upgrade agy; capture one transcript's key paths; settle Gate G4. |
| 2 | Parallel run for at least one working day: `span_dedup` drops per span equal to receiver spans for turns the hook also sent; compare a sample of traces in Langfuse. | Remove the plugin marketplace entry, its hook trust and `langfuse.json` at T in one apply; remove `otlp/langfuse_plugins` from the edge once no plugin sends to it. | Render `agent_transcript/agy`; remove the dead hook entry and the `CC_LANGFUSE_*` block from `agy.fish`. |
| 3 | Wait until the spool is drained (`langfuse_replay` empty), then remove Stop/SessionStart/SessionEnd hook entries, the LaunchAgent, wrapper, replay runner, vendored hook, patches and state files. | Delete `<rollout>.langfuse` sidecars. | — |
| Gap/duplicate | No gap: both paths cover every turn during the overlap; duplicates on the Langfuse leg are dropped by ID. | No gap by the overlap; at most 5 minutes of turns duplicated in Langfuse, because plugin IDs are random. | None. |

Codex native logs and metrics stay as they are; native traces stay off.

### Recommendation

Build one `agent_transcript` receiver type with per-engine instances, a shared
core (two read modes, checkpoint-after-admission, ID policy, batching under the
gateway cap, five-layer drift detection) and three decoders, in the order
Claude Code, Codex, agy; publish it and `span_dedup` contrib-style in lockstep
with Collector core, and keep the auth-retry exporters unpublished.

### Options Considered

1. **Three receivers, one per engine** — rejected by the owner's decision:
   durability, IDs and failure handling would be triplicated.
2. **Keep hooks, make them thin (hook → OTLP to the edge)** — removes the
   spool but keeps per-engine firing models, partial-turn bookkeeping and the
   dependence on hooks firing at all; agy's hook payload differs anyway.
3. **Extend `filelog` with an OTTL mapping** — filelog advances its offset on
   consumer failure (`pkg/stanza` `reader.go:283-305`, `adapter/receiver.go:68-86`)
   and produces logs, not span trees with derived parent links.
4. **This proposal** — the core owns everything engine-independent; engines
   own decoding and mapping only.

### Risks And Decision Gates

Owner decisions, each with a recommendation:

- **G1 — Publication.** Publish `agent_transcript` and `span_dedup`
  contrib-style, keep the auth-retry exporters unpublished, lockstep `v0.x`
  versions, CI-cut per-module tags (Finding 10). Recommended: yes, as written.
- **G2 — Generation input.** Delta (new user message or tool results) instead
  of the hook's full session history on every generation. Recommended: delta;
  history duplication grows quadratically in Langfuse storage, and IDs are
  unaffected either way.
- **G3 — Hook-only Claude modes.** Drop attached mode
  (`CC_LANGFUSE_TRACEPARENT`) and seed mode (`CC_LANGFUSE_TRACE_SEED`), which a
  tailer cannot see. Recommended: drop, unless something outside the e2e tests
  relies on them.
- **G4 — agy content.** First release transcript-only (turns, planner text,
  tools; model and usage only if the current agy writes them), no SQLite
  protobuf decoding. Recommended: yes, after one session on the current agy
  confirms the fields and `step_index` stability.
- **G5 — Tree shape for Codex and agy.** Use the Claude-shaped tree (tools as
  siblings of the generation) for all engines rather than the Codex plugin's
  nesting under the generation. Recommended: Claude shape, so dashboards and
  filters work across engines.
- **G6 — Codex cutover overlap.** Accept up to 5 minutes of duplicated Codex
  turns in Langfuse at cutover rather than risk a gap. Recommended: accept.

Risks: all three formats are internal (Finding 08 is the mitigation); the
Claude collision suffix is order-dependent in the hook (Finding 06); the dedup
TTL bounds the replay window (PC-19); agy `step_index` stability under
compaction is unknown (PC-24).

## Sources And Limits

- devbox-setup (read-only): `roles/devbox/files/dot_claude/bin/vendor/langfuse_hook.py`
  and `vendor/patches/0001`–`0008`; `dot_claude/bin/langfuse_trace_hook.py`,
  `langfuse_replay.py`; `dot_claude/settings.json.j2`; `dot_codex/langfuse.json.j2`,
  `config.toml.j2`; `defaults/main/codex.yml`; `dot_agy/config/hooks.json.j2:175-183`,
  `dot_agy/bin/vendor/README.md`, `.config/fish/functions/agy.fish`;
  `tasks/darwin/configure_gemini_telemetry.yml`. Patch 0008 lines P8:43-121,
  139-186, 348-406.
- `langfuse/codex-observability-plugin` at `46e3936b`: `src/parse.ts`,
  `src/trace.ts`, `src/sidecar.ts`, `src/config.ts`.
- `openai/codex` `main` at `67f0994b`: `codex-rs/rollout/`, `codex-rs/history/`,
  `codex-rs/protocol/src/protocol.rs`, `codex-rs/otel/`; installed CLI 0.162.1.
- agy 1.2.10 binary strings (embedded docs and hook spec), upstream
  `CHANGELOG.md`, issue `google-antigravity/antigravity-cli#366`.
- Collector modules at the `builder.yaml` pins: `pkg/stanza@v0.163.0`
  (`fileconsumer`, `adapter`), `extension/xextension@v0.163.0/storage`,
  `receiver/receiverhelper@v0.163.0`; contrib `docs/new-components.md`,
  collector `docs/component-stability.md`.
- Local transcripts were surveyed for structure only (record types, field
  names, versions); no content was read into this note.
- Not yet proven: agy ≥1.4 transcript fields and `step_index` stability under
  compaction; whether Codex fires `Stop` for subagent threads (irrelevant to
  the receiver, relevant to the plugin's current duplicates); the hook-vs-port
  ID parity, which the implementation proves with test vectors.
