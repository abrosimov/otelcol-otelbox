# Task: define strategic trace sampling

## Status

Deferred. This is not a 2.1 release blocker.

## Problem

The gateway currently treats eligible traces as required delivery. Tail
sampling before a persistent exporter can acknowledge a trace while the
sampling processor still holds its decision state only in memory. Silently
adding it would change both durability and cost semantics.

## Candidate placements

- head or probabilistic sampling in the SDK or edge, where reduced delivery is
  explicit before gateway acceptance;
- stateless sampling on an explicitly lossy derivative branch;
- a dedicated tail-sampling tier with its own availability and persistence
  contract.

The canonical required-delivery pipelines must not acquire in-memory tail
sampling as an incidental processor insertion.

## Admission criteria

- measured trace volume, storage cost and target reduction;
- policy for errors, latency outliers and representative successful requests;
- deterministic routing of every span in a trace to the same decision maker;
- crash, overload and decision-buffer loss semantics;
- separate names and documentation for required and sampled delivery.

Reopen when measured traffic or backend retention cost justifies a lossy path.

