---
note_id: "20260907185000"
created: 2026-09-07
type: meta
subtype: standard
gov_self_evolvement: false
retention: active
language: en
status: active
---
# Agent contributions

Agent-authored notes use the same vault as human-authored material. The shared
`agent-artifact` subtype declares `authorship: agent`, the producing `assistant`,
artifact kind, sources, publication state, and supersession fields. They do not
create a parallel agent knowledge base.

Placement is owned by the adopting project. The template defines a path only for
the Katoptron subtype in [[katoptron-postmortem]]. Research, proposals, reviews, and other
retrospectives follow a project rule that is both stated and validated.

Every material claim carries an adjacent recoverable source and locator or an
explicit evidence gap. Human assessment is an append-only event only when the
subtype declares and validates that event, or otherwise a separate linked review.
There is no manually maintained current-verification field. Review does not
change authorship and does not make unrelated claims true.

Drafts are prepared before landing. On the default branch, bodies follow
[[note-lifecycle]]. A changed conclusion creates a successor; feedback uses only
the append-only event form declared by the subtype or a separate linked review.
Agents do not author human assessment or rewrite human prose.

`publication_status: draft` exists only during preparation. Before first landing
it becomes `published` and carries `published_at`; those facts are not repeated
as a mutable body section.

First-party authored material is English. Verbatim source excerpts and operator
feedback may retain their source language when visibly identified as records.
Translation is a separate authored claim and must not be silently substituted for
the source.

Templates define required structure, not a target length. Omit inapplicable
optional elements instead of padding them. Machine validation proves structure
and declared relationships, not the truth or quality of prose.
