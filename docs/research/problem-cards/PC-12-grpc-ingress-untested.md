---
note_id: "20261022121200"
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
# PC-12: OTLP/gRPC ingress has no black-box durability evidence

## Objective

Record one problem-side claim of the durable-admission audit as an FPF C.22.2
ProblemCard in its thin form, with the evidence that currently supports it.

## Output

### Finding 01: problem card

| Field | Value |
| --- | --- |
| Signal | Every harness test posts OTLP/HTTP JSON; gRPC appears only on the edge-to-gateway leg, where the edge's exporter is the client. |
| Entity of concern | The harness's ability to send a protobuf OTLP request over gRPC and observe its status, for edge and gateway ingress. |
| Scope | `test/harness` (stdlib-only module); edge port 4317, gateway authenticated gRPC port. |
| Claim family | Method-availability claim: a stdlib-only client can speak unary gRPC over unencrypted HTTP/2 with hand-encoded protobuf. |
| Why not a ticket | Adding grpc-go would break the harness's stdlib-only contract. |
| Acceptance probe | A gRPC export acknowledged while the downstream is down survives SIGKILL, for edge and gateway, and the client observes the gRPC status. |
| Next use | Closed for the receiving use. |

### Evidence

| Claim | Proof | Status |
| --- | --- | --- |
| Harness posts use `postOTLPJSON` only. | `test/harness/otlp_test.go:304-320` | supported by inspected source |
| The gRPC service uses the same `Export` functions as HTTP. | `go.opentelemetry.io/collector/receiver/otlpreceiver@v0.162.0/otlp.go:96-113` | supported by inspected source |
| Logs, spans and metric points acknowledged over OTLP/gRPC survive SIGKILL at the edge and the gateway; the gateway answers a tokenless gRPC export with `Unauthenticated`. | `TestEdgePersistsAcceptedDataBeforeAcknowledgement`, `TestGatewayPersistsOrdinarySignalsBeforeAcknowledgement`; client in `test/harness/grpc_test.go` | proven by test |

### Interpretation

The seam is a standard-library unary gRPC client; compression on the producer leg is not exercised and stays covered by source only.

## Sources And Limits

Upstream paths are module paths in the Go module cache at the versions
`builder.yaml` resolves. Status values follow the audit's evidence scale:
proven by test, supported by inspected source, assumed from external
contract, not yet proven.

Go 1.24+ `net/http` supports unencrypted HTTP/2 through `http.Protocols`.
