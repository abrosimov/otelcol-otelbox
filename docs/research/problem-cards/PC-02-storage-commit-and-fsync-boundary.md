---
note_id: "20261022120200"
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
# PC-02: What a committed queue write survives

## Objective

Record one problem-side claim of the durable-admission audit as an FPF C.22.2
ProblemCard in its thin form, with the evidence that currently supports it.

## Output

### Finding 01: problem card

| Field | Value |
| --- | --- |
| Signal | Every profile sets `fsync: true`. The repository never states what that setting makes durable or what it assumes about the storage below it. |
| Entity of concern | One `file_storage` client `Batch` call as implemented by the filestorage extension over bbolt: its transaction boundary and the synchronisation performed before it returns. |
| Scope | filestorage v0.162.0, bbolt v1.5.0, Linux and macOS, a local filesystem; process crash, operating-system crash and power loss. |
| Claim family | Anticipated-condition claim with an external assumption: after `Batch` returns, the data and meta pages were passed to `fdatasync`, so the write survives a process crash, and survives power loss only when the device honours the flush. |
| Why not a ticket | Setting `fsync: true` is configuration, not evidence of the guarantee. |
| Acceptance probe | Source references for the transaction and the synchronisation calls; a SIGKILL test for process crash; power-loss durability recorded as an assumption, not as a test result. |
| Next use | Characterise: state the assumption in the audit; no code change. |

### Evidence

| Claim | Proof | Status |
| --- | --- | --- |
| `fsync: true` sets bbolt `NoSync=false`; `NoGrowSync` stays false; `NoFreelistSync` is always true. | `github.com/open-telemetry/opentelemetry-collector-contrib/extension/storage/filestorage@v0.162.0/client.go:51-62` | supported by inspected source |
| One client `Batch` is one bbolt `db.Update` transaction. | `github.com/open-telemetry/opentelemetry-collector-contrib/extension/storage/filestorage@v0.162.0/client.go:118-131` | supported by inspected source |
| `Tx.Commit` writes and `fdatasync`s data pages, then writes and `fdatasync`s the meta page, before returning. | `go.etcd.io/bbolt@v1.5.0/tx.go:170-282`, `:568`, `:615`; `go.etcd.io/bbolt@v1.5.0/bolt_linux.go:8` | supported by inspected source |
| A crash before the meta page write leaves the previous commit valid. | `go.etcd.io/bbolt@v1.5.0/tx.go:170-282` | supported by inspected source |
| The device and filesystem make `fdatasync`ed data durable across power loss. | operating-system and hardware contract | assumed from external contract |
| On macOS bbolt's `fdatasync` is `os.File.Sync`, which Go implements with `fcntl(F_FULLFSYNC)` and so flushes the drive cache. | `go.etcd.io/bbolt@v1.5.0/boltsync_unix.go:6-8`; Go 1.27.3 `src/internal/poll/fd_fsync_darwin.go:16-22` | supported by inspected source |

### Interpretation

Against process crash the boundary is sound and tested. Against power loss it rests on the device honouring the flush the code requests, on Linux through `fdatasync` and on macOS through `F_FULLFSYNC`.

## Sources And Limits

Upstream paths are module paths in the Go module cache at the versions
`builder.yaml` resolves. Status values follow the audit's evidence scale:
proven by test, supported by inspected source, assumed from external
contract, not yet proven.

Power-loss behaviour is not testable in the harness and stays an assumption.
