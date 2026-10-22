# Agent Guide

This repository is **otelcol-otelbox**. Its documentation vault uses Kadmos
distribution `0.2.0` for structure, authoring rules, and
deterministic validation. Kadmos is tooling used by this project; it is not this
project's identity.

## Vault boundary

The directory containing this file is the vault root and carries `.vault-profile`.
When it is an immediate child of a larger repository, the parent repository must
name it in `.vault-root`. A child `.vault-profile` alone does not retarget
repository operations. The parent marker names one immediate, non-symlink,
non-dot-prefixed child and cannot coexist with a root `.vault-profile`.

## Authority

- The user controls goals, scope, acceptance, and external effects.
- Read the applicable standard before creating or changing a note.
- Preserve human authorship and human-owned assessment records.
- Do not create a task system, state snapshot, manual index, or additional
  content hierarchy unless this project defines and validates one.

Project-specific agent instructions belong in the descendant-owned
`PROJECT_AGENTS.md`. Read it when present. It may add constraints and placement
rules but cannot redefine this Kadmos-owned contract. Do not edit managed files
to add project policy; their hashes are checked against the distribution receipt.
`PROJECT_AGENTS.md` is explicitly outside the governed note population.
The generated `.cadmus-manifest.json`, not the receipt, declares the selected
Kadmos profile, placement policy, expected files, and capability membership.

## Notes

- Follow `standards/note-lifecycle.md` for immutability and supersession.
- Follow `standards/agent-contributions.md` for agent-authored material.
- Use short notes and link to existing notes instead of reproducing their prose.
- Write first-party documentation in English. Verbatim source material may retain
  its source language when its role is explicitly declared.
- Treat a broken internal link as a validation failure.

## Katoptron

Katoptron records an authority incident identified by the operator. The model or
agent whose actions caused it writes its own postmortem on operator request.
Read `standards/katoptron-postmortem.md` and use the bundled authoring template
and instructions. Preserve the session ID and complete the required causal
analysis, self-reflection and prevention options without self-exoneration.
Human feedback is free-form. The first requested case creates `katoptron/`;
an empty corpus is not required.

## Validation

Validate with the external validator Lynkeus (`lynkeus-kadmou`). Kadmos distributes declarations, not an
executable validator or fallback language toolchain. Validation reports structure and consistency; it does not establish
the truth of prose claims.
