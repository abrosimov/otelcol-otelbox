---
note_id: "20261022120600"
created: 2026-10-22
type: note
subtype: agent-artifact
language: en
retention: active
note_role: artifact
artifact_kind: research
publication_status: draft
status: active
authorship: agent
assistant: Claude Code
---
# PC-06: Compaction keeps acknowledged records across a crash

## Objective

Record one problem-side claim of the durable-admission audit as an FPF C.22.2
ProblemCard in its thin form, with the evidence that currently supports it.

## Output

### Finding 01: problem card

| Field | Value |
| --- | --- |
| Signal | Every WAL enables `compaction.on_start` and `on_rebound`; no test exercises compaction. |
| Entity of concern | One filestorage compaction of one bbolt file: copy to a temporary file, rename over the original, reopen. |
| Scope | filestorage v0.162.0 on one local filesystem with `compaction.directory` beside the data, as both profiles configure; process crash and power loss. |
| Claim family | Anticipated-condition claim: a process crash at any point leaves either the original or the complete copy; power loss after the rename can resurrect the original because the directory is not synchronised. |
| Why not a ticket | "Test compaction" does not say which crash class the guarantee covers. |
| Acceptance probe | Source references for the rename and cleanup; a classification of power-loss behaviour as not proven; a decision whether a SIGKILL-during-compaction test is worth its cost. |
| Next use | Characterise; a test only if the owner accepts a 100 MiB fixture, because rebound compaction needs a file of at least 100 MiB. |

### Evidence

| Claim | Proof | Status |
| --- | --- | --- |
| Compaction copies into `tempdb*` in `compaction.directory`, closes both files and renames the copy over the original. | `github.com/open-telemetry/opentelemetry-collector-contrib/extension/storage/filestorage@v0.162.0/client.go:224-315` | supported by inspected source |
| A crash before the rename leaves the original intact; `cleanup_on_start` removes the temporary file. | `github.com/open-telemetry/opentelemetry-collector-contrib/extension/storage/filestorage@v0.162.0/extension.go:53-58`, `:243-267` | supported by inspected source |
| The parent directory is not synchronised after the rename. | `github.com/open-telemetry/opentelemetry-collector-contrib/extension/storage/filestorage@v0.162.0/client.go:224-315` | supported by inspected source |
| Writes wait on a lock during compaction instead of failing. | `github.com/open-telemetry/opentelemetry-collector-contrib/extension/storage/filestorage@v0.162.0/client.go:118-131` | supported by inspected source |
| A cross-filesystem rename falls back to an unsynchronised in-place rewrite. | `github.com/open-telemetry/opentelemetry-collector-contrib/extension/storage/filestorage@v0.162.0/client.go:398-428` | supported by inspected source |
| Records acknowledged before a SIGKILL during compaction are delivered after restart. | no test | not yet proven |

### Interpretation

Process crash is safe by construction. Power loss after a compaction may roll the file back to its pre-compaction state, which can resurrect delivered records (duplicates) and, if writes followed the rename, lose them.

## Sources And Limits

Upstream paths are module paths in the Go module cache at the versions
`builder.yaml` resolves. Status values follow the audit's evidence scale:
proven by test, supported by inspected source, assumed from external
contract, not yet proven.

Deployments must keep `compaction.directory` on the same filesystem as the data.
