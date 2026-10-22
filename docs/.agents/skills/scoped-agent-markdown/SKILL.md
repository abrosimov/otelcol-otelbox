---
name: scoped-agent-markdown
description: Apply the agent-authoring contract when creating or changing Markdown. Use for agent-authored research, proposals, reviews, retrospectives, publication state, provenance, feedback, or Markdown placement.
---
# Scoped Agent Markdown

Use this skill before an agent creates or changes Markdown in this repository.

Read [the agent contribution standard](../../../standards/agent-contributions.md)
and [the note lifecycle](../../../standards/note-lifecycle.md) first. They define
the source-of-truth and editing boundaries; this file is a practical checklist.

## Where an agent may write

**This contract does not fix a location.** Placement is a decision of the
repository that adopts the contract. Before writing, find the repository's
stated rule — in its `AGENTS.md` or its standards — and follow it.
If a repository has not stated one, ask rather than inventing a directory.

The contract declares no general location for agent output. Do not create a
global `AI/`, `Agents/`, or artifacts hierarchy, and do not import a descendant's
hierarchy on the assumption that it is shared. `archive/` and `graveyard/` are
retention projections governed by the note lifecycle, not output locations.
When an authorised retention change moves `path/to/note.md`, preserve that path
below `archive/` or `graveyard/`; change only frontmatter and location, never
the landed body. Run reference validation on the planned result and stop the move
when a path-qualified link would break.

## What an artifact must carry

Use `templates/ai_research_template.md`, `ai_proposal_template.md`,
`ai_review_template.md`, or `ai_retrospective_template.md` as the starting
point. `templates/ai_artifact_template.md` is the generic fallback.

Keep the required frontmatter intact:

- `subtype: agent-artifact` for the shared research, proposal, review, and
  retrospective templates;
- `note_role: artifact` and `artifact_kind` — one of `research`, `proposal`,
  `review`, `retrospective`;
- `publication_status` — `draft` or `published`;
- `retention: active` until an authorised retention change;
- `authorship: agent`, and name the producing `assistant`;
- `source_refs` — list source notes where they exist; never invent them;
- `status: active`, with `superseded_by` and `evolved_from` carrying successor
  relationships when applicable;
- no mutable verification field; human assessment uses the subtype's validated
  event or a separate linked review.

Katoptron follows `standards/katoptron-postmortem.md`: the participant writes its
own postmortem on operator request and preserves the incident `session_id`.
`source_refs` keeps its shared meaning.

Where a repository keeps a human-owned briefing or review ledger beside the
work, an agent may append one factual row to its run log. Do not rewrite its
objective, sources, or human-owned assessment.

## Boundaries

- Do not create a second task system, a manual index, or a parallel knowledge base.
- Do not create or change human-authored notes, decisions, task status, or
  completion state unless the user explicitly requested that exact change.
- Do not edit a note merely to make a graph or index look fuller.
- Never author human assessment of your own output.
- Do not fabricate sources, citations, decisions, or completion status.

## Publication, feedback, and language

- `publication_status: draft` is pre-landing work. Complete publication metadata
  before the note first lands on the default branch.
- Do not correct a published conclusion in place. Create a successor with
  `evolved_from` when it replaces the prior conclusion, or use `source_refs`
  for a focused follow-up to one finding.
- After landing, feedback is a separate linked review artifact unless the
  subtype declares a specific append-only event section. Katoptron declares
  `Review Thread` with free-form `Feedback NNN` entries; existing entries are
  never rewritten, and no date, authority or target fields are mandatory.
- Write new first-party artifact bodies in English. Verbatim evidence and
  operator-feedback records may retain their source language when visibly
  identified as records.
- Put a proof link and locator beside every material research claim. Do not rely
  on unlinked memory or collect links only in a final source dump.

## Before handing off

Validate with the external validator Lynkeus (`lynkeus-kadmou`). There is no local fallback validator;
while integration is pending, report that evidence limit rather than inventing a
check.
