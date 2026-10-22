# otelcol-otelbox vault rules

These rules add to the Kadmos contract in `AGENTS.md`; they do not redefine it.
The repository-wide operating contract is the repository root `AGENTS.md`.

## Kadmos source

The managed files were copied from Kadmos `master` at `fcd0d58`, distribution
`0.2.0`, by hand. The materialiser receipts are absent because the materialiser
is external and not yet available; managed-file hashes are therefore unchecked.
External validation by Lynkeus is not run yet. Treat both as known gaps until
that automation arrives, and update the managed files only from a Kadmos
revision, never by local edit.

## Placement

| Directory | Holds | Body mutation |
| --- | --- | --- |
| `reference/` | `reference` notes: the role guides, the redaction boundary and the adoption guide. They describe the current profiles and change in the same commit as the profile they describe. | `mutable` |
| `roadmaps/` | `roadmap-item` notes: the roadmap and, under `tasks/`, deferred design tasks. | `immutable` |

A new kind of note gets a directory of its own with a `.vault-spec.txtpb`
declaring it, and a row here. Agent-authored research uses the Kadmos
`agent-artifact` kind from `templates/ai_research_template.md`; its directory is
declared here when the first such note lands.

## Known schema findings

- `reference/` declares `mutable`, but Kadmos D-040 requires an ancestor grant
  and the managed root `.vault-spec.txtpb` grants none. Kadmos has no
  descendant extension point yet, so the finding stays until it does.
- The managed root `admits` list does not register `reference` or
  `roadmap-item`; the directory specifications declare them, for the same
  reason.
