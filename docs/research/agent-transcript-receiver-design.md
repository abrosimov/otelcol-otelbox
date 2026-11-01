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
# Proposal — One agent-transcript receiver for Claude Code and Codex

## Objective

Replace the per-engine Langfuse delivery paths on workstations with one
edge-role receiver, `agent_transcript`, that tails coding-agent transcripts,
decodes them with one decoder per engine, and emits OTel spans carrying
Langfuse attributes and deterministic identifiers into a dedicated edge
pipeline:

```text
transcript files -> agent_transcript/<engine>
  -> traces/agent_transcripts: memory_limiter -> resource_detection -> redaction/secrets
  -> otlp_grpc/gateway (file_storage WAL)
  -> gateway traces/selected -> span_dedup -> otlp_http/selected_traces (Langfuse)
```

The pipeline is separate from the edge `traces` pipeline because
`transform/langfuse_native` there overwrites `langfuse.user.id` and
`langfuse.trace.tags` on every span (`config/edge.yaml:186-190`), and the
repository contract forbids it on a pipeline fed by a Langfuse producer.

Scope of this proposal: Claude Code first, Codex second. agy is deferred
(Finding 01). Publication of modules is out of scope for now.

Shared: tailing, checkpointing, identifier derivation, failure handling and
drift counting. Engine-specific: locating and decoding transcripts and
mapping them to an engine-neutral observation model.

This revision folds in the product, domain, domain-model and architecture
reviews of the first draft and the owner's remarks on it.

## Output

### Finding 01: today's paths, measured

| Engine | Current path | Durability | Identifiers | Size: mapping vs infrastructure |
| --- | --- | --- | --- | --- |
| Claude Code | Stop/SessionStart/SessionEnd hook running the vendored `langfuse/claude-observability-plugin` 1.2.1 with patches 0001–0008, a wrapper, a spool and a 5-minute LaunchAgent replay runner. | Its own: fsync checkpoints, local OTLP spool, transcript snapshots, pending markers, replay. | Deterministic (patch 0008). | Hook 3,720 lines: 2,716 mapping (~340 of them exist because of the hook-firing model), 942 infrastructure, 62 tailing; plus 643 infrastructure lines outside the hook. Infrastructure ≈ 1,650 of 4,360 lines (38 %). |
| Codex | Stop hook running `langfuse/codex-observability-plugin` at `46e3936b` (v0.1.0), re-parsing the whole rollout per firing. | None beyond a `<rollout>.langfuse` sidecar. | Random span IDs; random trace IDs (no `trace_seed`). | 1,113 lines: 480 decoding, 222 mapping, 411 infrastructure (37 %). The pin is 39 commits behind upstream v0.4.3, which fixed ancestor-turn skipping (#89), forked-subagent attribution (#97) and nesting under `spawn_agent` (#109). |
| agy (deferred) | The Claude hook, reused through a symlink, registered on `Stop` and disabled since devbox-setup `01003b2`. | — | — | The reuse was proposed in an earlier session on the expectation that agy's hook payload and transcript matched Claude Code's. They do not: agy sends `conversationId` and writes `USER_INPUT`/`PLANNER_RESPONSE` steps without a `message` object, its transcript is rewritten on compaction and written out of `step_index` order, and on 1.2.10 it carries no model or usage. agy needs its own decoder and a non-append read mode; it is deferred. |

The receiver removes the infrastructure column: the edge already owns durable
admission (PC-01), retry, authentication recovery and the gateway leg.

### Finding 02: native telemetry of Codex is not sufficient

| Need for a Langfuse trace | Codex native OTel (`codex-rs/otel`) |
| --- | --- |
| Model input and output per call | No. Prompt text only in logs with `log_user_prompt`; model output never, apart from the opt-in final answer `codex.agent_response`. |
| Tool calls with arguments and results | In `codex.tool_result` logs only; spans carry lengths. |
| Token usage on a generation | Only on the internal `handle_responses` span. |
| Deterministic span IDs | No; SDK-random. |

Codex's native logs and metrics keep flowing to the edge unchanged. agy has no
OTLP export at all (Clearcut only, `google-antigravity/antigravity-cli#366`);
this is recorded for the deferred agy work.

### Finding 03: Claude Code transcripts

| Aspect | Fact | Status |
| --- | --- | --- |
| Main transcript | `~/.claude/projects/<cwd with non-alphanumerics as ->/<session-uuid>.jsonl` | documented |
| Subagents | `<project>/<session>/subagents/agent-<id>.jsonl` with `agent-<id>.meta.json` (`toolUseId`), flat, nested by `spawnDepth` (2 in 114 files, 3 in 53 locally); workflow agents under `subagents/workflows/<runId>/` whose metas have no `toolUseId` | location documented; layout undocumented |
| Excluded files | `<session>.orphaned-<ts>-<suffix>.jsonl`, `<session>.jsonl.superseded-<ts>` | documented |
| Lifecycle | Append-only per session; resume appends; deletion after `cleanupPeriodDays` (devbox: 3650) | inferred + documented |
| Row schema | Internal. `version` on every chat/system/attachment row (2.1.220–2.1.291 locally; files can mix versions). | undocumented |
| Turn end | `system`/`turn_duration` row, 1,838 locally against ~2,053 top-level prompts, carrying `pendingBackgroundAgentCount` and `pendingWorkflowCount`; 991 of 1,838 report pending background agents | observed, undocumented |
| Copied rows | Rows whose `sessionId` differs from the file stem are history copied from another session; the hook drops them. None found locally. | hook rule, no local fixture |

### Finding 04: Codex rollouts, three generations

Files: `$CODEX_HOME/sessions/YYYY/MM/DD/rollout-<UTC ts>-<thread_id>.jsonl`
(`codex-rs/rollout/src/lib.rs:86-87`, `rollout_file_name.rs:62-73`). Resume
appends to the same file and may write `session_meta` again; archive renames
into `archived_sessions/`; revert writes a new file
`rollout-<ts>-<thread_id>_<rollout_id>.jsonl`; forks and subagents get a new
file that first copies the parent's records. Zstd compression and the
paginated migration rewrite exist behind features that are off by default
(source claim; unverified locally). The schema is internal; the hidden
`codex app-server generate-internal-json-schema` emits it.

Local corpus (1,635 rollouts, `session_meta` first in every file checked):

| Generation | `cli_version` seen | Distinguishing records |
| --- | --- | --- |
| no `history_mode` | 0.115–0.142 | most files lack `session_meta.session_id`; `exec_command_end`, `web_search_end`, `user_message`, `agent_message` |
| `legacy` | 0.144–0.151 | adds `sub_agent_activity`, `thread_settings_applied`, `world_state`, `response_item/agent_message`; `task_started.trace_id` in 1,144 of 1,583 |
| `paginated` | 0.147–0.166 | line `ordinal`; `item_completed` (`AgentMessage`, `CommandExecution`, `Reasoning`, `SubAgentActivity`); no `user_message`; `token_usage_record` only from 0.153.4 |

What a multi-version decoder must handle:

- `history_mode` never mixes inside one file, but `cli_version` does: 105 files
  hold two versions because a newer binary resumed the session. The variant is
  chosen per file by `history_mode` and refined per `session_meta` segment by
  `cli_version`.
- Several writers at once: `Codex Desktop`, `codex-tui`, `codex_exec`, VS Code.
  The installed CLI (0.162.1) is not the range in use (files reach 0.166.0).
- `multi_agent_version` absent, `disabled` or `v2`.
- 1,152 of 1,622 files carry an inherited parent prefix: 1,150 paginated
  subagents marked by `subagent_history_start_ordinal`, 65 legacy forked
  subagents without a marker.
- About 630 paginated turns never reach `task_complete` or `turn_aborted`, so
  an idle bound is mandatory for Codex.

### Finding 05: domain model

| Term | Definition |
| --- | --- |
| Source file | One transcript, identified by a `SourceKey` derived from content (session or thread ID in the head record plus a fingerprint of the head bytes), not by path. |
| Record | One newline-terminated line with its byte span `[start,end)`. An unterminated tail is not a record. |
| Turn | One user prompt and all work done for it; one trace. |
| Observation | One Langfuse span, generation or tool, with a stable span key and parent key; no OTel types. |
| Emission | A set of observations passed to `ConsumeTraces`, plus the cursor it permits. An emission is not a turn. |
| Cursor | A byte offset; every record before it is either emitted or reproducible from the carry. |
| Carry | Decoder state stored with the cursor: turn counter, recorded close points, pending async markers. |
| Checkpoint | `{cursor, carry}`, committed atomically per `SourceKey`. |
| Admission | `ConsumeTraces` returned nil, i.e. the spans are in the edge WAL (PC-01). |

Invariants:

| ID | Invariant | Owner |
| --- | --- | --- |
| INV-1 | A committed cursor never passes a record whose observations are not admitted. | shared core |
| INV-2 | The committed cursor is the low-water mark: never beyond the first record of any open turn, including a turn held open for an async agent while later turns run. | decoder computes, core enforces |
| INV-3 | IDs are a function of record content and carry only: never wall clock, read chunking, restart point or process. | ID policy + decoder |
| INV-4 | An observation is emitted only when its content is final: a tool after its result, a generation after its last row, a root after its turn closes. | decoder |
| INV-5 | When a turn is closed by a bound (idle or size) rather than by the source, the close point is stored in the carry, so a re-read reproduces the same close and the same root content. | decoder (carry) |
| INV-6 | Unknown, malformed or panicking records are counted and passed; they never hold the cursor back except through an open turn. | decoder + core |
| INV-7 | No two live files share a `SourceKey` (Codex revert files share a thread ID and need the `_<rollout_id>` suffix in the key). | core, engine identity rule |
| INV-8 | Any replay window is shorter than the effective dedup window: the smaller of the `span_dedup` TTL (72 h) and the age at which `max_entries` (500,000) eviction starts. | deployment configuration |

INV-1 relies on the pipeline having no asynchronous stage; AGENTS.md already
forbids `batch` in role pipelines.

### Finding 06: the decoder interface

One component type, `agent_transcript`; one instance per engine
(`agent_transcript/claude_code`, `agent_transcript/codex`) so each engine is
switched on independently and has its own metrics and storage client. One
read mode: `append` (byte-offset tailing, partial line held back). The
interface does not assume that the only future read mode is append.

```go
type Engine interface {
	Name() string
	Sources(cfg EngineConfig) SourceSpec // include/exclude globs
	Identify(path string, head []byte) (SourceKey, error)
	NewDecoder(src SourceInfo, carry []byte) (Decoder, error)
}

// SourceInfo carries a read-only view of the engine root, so the Claude
// decoder can open a session's subagent files.
type Decoder interface {
	Feed(rec Record) Outcome // never fails the source
	Ready(idle time.Duration) Emission
}

type Emission struct {
	Observations []Observation
	LowWater     int64  // cursor permitted once Observations are admitted
	Carry        []byte
}
```

What a further engine adds: an `Engine`, its seeds under its own namespace,
fixtures per format generation, and a harness fixture. No storage, delivery,
ID or drift code.

### Finding 07: mapping

The observation tree is the one the Claude hook produces, because Claude's
identifiers depend on parent links:

```text
trace (one per user turn)
└── root span "<Engine> Turn"   parent = phantom "carrier" span id (never exported)
    ├── generation "LLM Call"   one per model response
    ├── tool "Tool: <name>"     sibling of the generation that called it
    └── span "Subagent: ..."    sibling of its Tool span; children recurse
```

Trace-level attributes on every span (`langfuse.trace.name`, `session.id`,
`langfuse.user.id`, `langfuse.trace.tags` as a comma-separated string,
`langfuse.environment`), as Langfuse v4 requires them on every span that must
be queryable. Observation attributes as the Langfuse SDK encodes them
(`langfuse.observation.*`). Resource: `service.name=<engine>`,
`otelbox.telemetry.class=llm`.

| Element | Claude Code | Codex |
| --- | --- | --- |
| Turn open | Non-meta `user` row that is not a tool result, task notification or interrupt marker | `task_started` |
| Turn close | `system`/`turn_duration` with no pending background agents or workflows; otherwise held until the notifications resolve them; idle bound only for interrupted turns, close point recorded (INV-5) | `task_complete`/`turn_aborted`; idle bound for the ~630 that never close, close point recorded |
| Generation | One per `message.id` (split rows merged) | One per `token_usage_record` (≥0.153.4); earlier, one per `token_count` step |
| Tool | `tool_use` ↔ `tool_result` by `tool_use_id` | call ↔ output by `call_id` |
| Subagent | Subagent files opened through `SourceInfo`; the parent turn stays open until each subagent file reaches its terminal record; recursion to any depth | Separate thread file placed into the parent trace; inherited prefix skipped |
| Timestamps | Row `timestamp`, µs; fallback parent start, then 1970-01-01; never wall clock | Line `timestamp`; `item_completed` timings where present |

For Codex, the current upstream plugin (v0.4.3) is consulted next to the pin
for the subagent and fork attribution it has since fixed.

### Finding 08: deterministic identifiers

`span_dedup` keys on `(trace_id, span_id)` (`internal/processors/spandedup/seen.go:17-28`).
That key is sufficient: every trace seed already contains the session or
thread identity, so adding `session_id` to the key would discriminate nothing,
and it would tie a generic processor to an attribute other producers may not
send. `session.id` stays an attribute on every span.

The one consequence that matters: deduplication keeps the first copy and
compares no content, and Langfuse v4 treats an accepted span as immutable
("do not export another record with the same span ID to add or change
attributes", Langfuse v4 OTel migration guide). So an ID is emitted only when
its content is final (INV-4). This is how the hook already works ("the root
exports exactly once", patch 0008).

**Claude Code — patch 0008, preserved exactly.**

```text
trace_id   = sha256(utf8("{session_id}:{turn_identity}"))[:16]
             turn_identity = user row uuid, else "turn:{N}" (N carried)
phantom    = derive_span_id(trace_id, 0, "carrier") or 1
derive_span_id(t, p, key, attempt=0):
             material = "{t:032x}:{p:016x}:{key}" + ("#{attempt}" if attempt)
             return big-endian uint64 of sha256(utf8(material))[:8]
collision  = while id == 0 or id == parent or id in issued(trace): attempt++
root key   = "turn-root:{session_id}:{turn_identity}"
keys       = "gen:{message.id}" | "gen:noid:{index}",
             "tool:{tool_use_id}" | "tool:{gen_key}:{i}",
             "subagent:{tool_use_id}", "workflow-agent:{tool_use_id}:{agentId}"
```

Locally no user row lacks a `uuid`, so the `turn:{N}` fallback is dormant.
The port is tested against vectors from devbox-setup
`bin/test_langfuse_deterministic_ids.py` and IDs captured from the hook.

**Codex — same construction, `codex:` namespace.** The trace owner is defined
once so that seeds and keys agree:

| ID | Derivation |
| --- | --- |
| trace seed | `codex:{session}:{turn}` where `session` = `session_meta.session_id`, else the root thread's `session_meta.id`; `turn` = `root_turn_id`, else `turn_id` |
| root key | `turn-root:{session}:{turn}` |
| generation key | `gen:{response_id}`, else `gen:{turn_id}:{step}` |
| tool key | `tool:{session}:{call_id}` |

Using `session` rather than the file's `thread_id` in every key makes an
inherited turn in an unmarked fork derive the parent's IDs, so `span_dedup`
drops it. Each fallback link is fixed by fixtures before any span ships;
changing a seed later splits every trace it touches.

### Finding 09: durability and failure model

> **S-T**: a record is covered by a committed cursor only after every
> observation derived from it is admitted. Re-emission is allowed; skipping is
> not, except where the source itself is gone.

The cards continue PC-01..PC-14 in thin C.22.2 form; each becomes a separate
note when the implementation provides its evidence.

| Card | Claim | Acceptance probe |
| --- | --- | --- |
| PC-15 Checkpoint after admission | Commit `{LowWater, Carry}` in one storage `Batch` only after `ConsumeTraces` returned nil. A crash in between re-emits identical IDs. | Harness: SIGKILL after enqueue and before commit; restart; delivered once at the Langfuse double. |
| PC-16 Refusal and back-pressure | An error leaves the cursor and retries with backoff; a call blocked on a full queue yields to the shutdown context. The transcript on disk is the buffer; nothing back-pressures the agent. | Harness: gateway down while a transcript grows. |
| PC-17 Partial last line | Never decoded until terminated. | Unit test. |
| PC-18 Rename, truncation, deletion | Identity by `SourceKey`, not path: a rename continues. Size below cursor or a changed fingerprint resets and re-reads from zero with identical IDs. Deletion loses only what the source no longer has; counted. | Harness: rename, truncate-and-rewrite, delete. |
| PC-19 Restart and first start | Restart resumes from the low-water cursor and re-emits the open turn's completed observations with identical IDs. First start reads sources modified within `initial_replay_window` (48 h, INV-8) from zero and fast-forwards older ones without emitting. | Harness: SIGKILL during tail. |
| PC-20 Format drift | See Finding 10; a decoder panic is recovered per record. | Unit: unknown types and fields. |
| PC-21 Open-turn bounds | Turns close by source signal; bound closes are recorded (INV-5); memory per open turn is bounded and a force-close is counted. | Unit. |
| PC-22 Checkpoint storage failure | A failed commit pauses the source and retries. The checkpoint store is a separate `file_storage/agent_transcripts` with `fsync: true`. | Unit with failing storage. |
| PC-23 Cross-file reads | Claude subagent files are read through `SourceInfo`; the parent turn stays open until they are terminal. | Fixture at subagent depth ≥ 2. |
| PC-25 Inherited history | Codex records before `subagent_history_start_ordinal` are skipped; unmarked forks derive parent IDs (Finding 08). | Fixture with a forked rollout. |
| PC-26 Redaction boundary | Input, output and metadata are span attributes and pass `redaction/secrets`; span names carry tool names and subagent descriptions, as the hook does. | Harness: secret in tool output masked. |

PC-24 (agy snapshot sources) is reserved for the deferred agy work.

Size: no receiver-side batching. The exporter's `sending_queue.batch` already
merges and splits after persistent enqueue
(`exporterhelper@v0.163.0/internal/queuebatch/partition_batcher.go:82,129`).
But a single item above its `max_size` (1.5 MiB) is dropped after
acknowledgement (`config/edge.yaml:323-326`), i.e. after the cursor moved, so
the receiver caps each span's encoded size well below that, in bytes.

Why not `filelog`: in `pkg/stanza@v0.163.0` the reader advances its offset
after an emit error (`fileconsumer/internal/reader/reader.go:283-287`,
`:303-308`), the adapter logs and swallows a `ConsumeLogs` error
(`adapter/receiver.go:68-86`), and the default emitter is asynchronous
(`adapter/factory.go:89-93`). The opt-in `retry_on_failure` only delays the
advance; permanent errors and shutdown still drop. Status: supported by
inspected source, not reproduced by a test. The weightier reason is that a
checkpoint must follow turns, not lines.

### Finding 10: drift detection

First release:

1. Per-record outcome counter
   `otelcol_receiver_agent_transcript_records_total{engine, outcome, record_type}`
   with outcomes `decoded`, `ignored_known`, `unknown_type`, `degraded`,
   `malformed`; unknown types bucket into `other` and each new name is logged
   once with its source version.
2. Source-version info gauge
   `otelcol_receiver_agent_transcript_source_version_info{engine, version, tested}`
   (Claude row `version`; Codex `cli_version` and `history_mode`), `tested`
   against the fixture range.
3. Invariant counter for a closed turn that had records but produced no
   observations — the decoder silently producing nothing.

Deferred: field-shape fingerprints, the remaining semantic invariants, and a
scheduled CI diff of Codex's generated schema (after the Codex decoder exists).

### Finding 11: duplicate windows

| Window | Covered by |
| --- | --- |
| Crash between enqueue and commit (PC-15) | `span_dedup` |
| Rewrite or truncation re-read (PC-18) | `span_dedup` |
| First-start replay (PC-19) | `span_dedup`, within INV-8 |
| Hook and receiver in parallel (Claude transition) | `span_dedup`; the first copy's content wins, so drop counts prove ID parity, not content parity |
| Unmarked Codex fork | `span_dedup` (Finding 08) |
| Gateway WAL resend after a lost recipient ACK | not covered (PC-11) |
| All-signal recipient | not covered; at-least-once by contract |
| Codex plugin → receiver overlap | not coverable (random plugin IDs); bounded overlap |

### Finding 12: implementation order and rollout

1. Shared core with a fake engine: append tailer, low-water checkpoint after
   admission, separate storage extension, ID policy, drift counters, per-span
   byte cap; `traces/agent_transcripts` in `config/edge.yaml`; `builder.yaml`.
2. Claude decoder: patch 0008 port with vectors, fixtures (subagent depth ≥ 2,
   async agents, interrupted turn), harness tests (SIGKILL during tail,
   restart, rename, truncate).
3. Codex decoder: paginated, then legacy and pre-`history_mode`, one fixture
   per generation and one with a mid-file version change.
4. Docs and CI contract; PC-15..PC-26 as separate notes as each gains evidence.

Rollout in devbox-setup (separate PR, later): Claude runs in parallel with the
hook until drop counts show ID parity, then the hook and its machinery are
removed after the spool drains; Codex switches at a planned time with a short
overlap because plugin IDs are random.

### Recommendation

Build the shared core and the Claude decoder now, then the multi-generation
Codex decoder, on the model above.

### Options Considered

1. Three receivers — rejected by the owner.
2. Thin hooks sending OTLP to the edge — keeps per-engine firing models and
   partial-turn bookkeeping.
3. `filelog` with OTTL — offset follows lines, not admission or turns.
4. This proposal.

### Risks And Decision Gates

- **Codex supported range.** Recommended: every generation present on the
  machine (0.115 to current), one fixture per generation; newer versions decoded
  best-effort with the version gauge.
- **Generation input.** Recommended: parity with the hook (full history) in the
  first release so the parallel run compares like with like; delta afterwards.
- **Hook-only Claude modes** (`CC_LANGFUSE_TRACE_SEED`, `CC_LANGFUSE_TRACEPARENT`).
  The wrapper and replay runner forward them; no caller in the personal
  workspace sets them. Recommended: drop, unless the work profile relies on
  them.

Risks: both formats are internal; Codex has three generations and several
concurrent writers; the dedup window bounds every replay.

## Sources And Limits

- devbox-setup (read-only): `dot_claude/bin/vendor/langfuse_hook.py`,
  `vendor/patches/0001`–`0008`, `langfuse_trace_hook.py`, `langfuse_replay.py`,
  `settings.json.j2`; `dot_codex/langfuse.json.j2`, `config.toml.j2`,
  `defaults/main/codex.yml`; `dot_agy/config/hooks.json.j2:175-183`,
  `dot_agy/bin/vendor/README.md`.
- `langfuse/codex-observability-plugin` at `46e3936b` and upstream #79, #89,
  #97, #109.
- `openai/codex` `main` at `67f0994b`: `codex-rs/rollout/`, `codex-rs/history/`,
  `codex-rs/protocol/src/protocol.rs`, `codex-rs/otel/`.
- Langfuse v4 OTel migration guide; "Simplifying Langfuse for Scale".
- Collector modules at the `builder.yaml` pins: `pkg/stanza@v0.163.0`,
  `exporter/exporterhelper@v0.163.0`, `extension/xextension@v0.163.0/storage`.
- Local transcripts surveyed for structure only.
- Not yet proven: hook-vs-port ID parity (proven by test vectors in step 2);
  Codex rewrite features off in practice; Claude copied-row rule (no local
  case).
