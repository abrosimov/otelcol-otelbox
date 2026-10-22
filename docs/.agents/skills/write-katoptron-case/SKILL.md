---
name: write-katoptron-case
description: Write the participant's own Katoptron postmortem when an operator identifies an authority incident and requests the record. Includes required session identity, causal analysis, self-reflection, prevention options and free-form human feedback.
---
# Write Katoptron Case

Read `../../../standards/katoptron-postmortem.md`,
`../../../standards/note-lifecycle.md` and
`../../../standards/katoptron_contract.txtpb` before authoring. The standard owns
meaning; the version 2 declaration owns machine-checkable body structure.

## Establish the incident and author

Treat the operator's request as the declaration of an authority incident and
authority to write its postmortem. Preserve the specific correction; do not
reopen whether an incident exists because its causes remain uncertain.

Write the case only when you are the model or agent whose actions caused that
incident. A different agent's review cannot replace the participant's account.
Do not open cases autonomously. Permission to write does not authorise repairs,
instruction changes or implementation of prevention options.

Resolve the vault selected by `.vault-profile` or the parent `.vault-root`.
Copy `templates/katoptron_case_template.md` to
`katoptron/<year>/<zero-padded-month>/<case_slug>.md` relative to that vault.
Use the creation year and month and a lowercase ASCII kebab-case slug. Create
the directory only for the first requested case.

## Preserve identity and establish the record

Set `assistant` to the participant author and preserve the incident `session_id`.
Record available `model_provider`, `model`, `reasoning_effort`, `interface`,
`cli_version` and `session_started_at` facts. Omit unavailable optional fields.
An unavailable session ID stays empty with an explicit gap in the body; the
mandatory identity requirement is unresolved and the case cannot be completed.

Locate the request, correction, material actions and consequences in recoverable
sources. Include precise locators and source versions where relevant. Do not
copy a transcript or create an evidence package. Do not include credentials,
personal data, absolute home paths or another note's prose.

## Write the analysis

1. Complete `What Happened` with the authorised scope, actual breach, operator
   correction, key sequence, actual impact and known gaps. Describe only response
   actions that occurred, their authority and observed results.
2. Complete 5–10 distinct `Cause NNN` entries in `Why`. For each explanation,
   supply the episode-specific basis and limits, including contrary evidence,
   a possible check or the remaining uncertainty. Do not pad the count.
3. Complete 3–5 `Option NNN` entries in `Prevention Options`. Link each to the
   causes it addresses, describe the change, its check and its costs or limits.
   Preserve alternatives and distinguish a proposal from adoption or completion.
4. Complete 5–10 `Reason NNN` entries in `Self-Reflection`. Analyse your own
   decisions and assumptions in concise retrospective accounts grounded in the
   available context. Explain misapplied constraints and missed signals without
   claiming to recover an unavailable internal process. This section is required
   independently of the causes.
5. Complete `Lessons Learned` for both model decisions and agent procedure.
   State the future situation and required response. Do not claim that writing
   the lesson establishes a durable behavioural change.

Number each entry family contiguously from `001`. Use local Wikilinks such as
`[[#Cause 001]]`. Incomplete content, duplicated explanations and invented facts
do not satisfy the ranges. The template is scaffolding, not a finished case.

## Check accountability

Name your actions and consequences directly. Remove self-exoneration, softened
descriptions of the breach, unsupported responsibility shifts and appeals to
good intentions as permission. If an instruction contributed, identify it and
explain why you chose to proceed despite the ambiguity. Technical capability
does not establish authorisation. Apologies and self-denigration do not replace
analysis.

## Complete and receive feedback

Before landing, resolve mandatory fields and content, then set
`publication_status: published` and `published_at`. These fields confer no Git
or external publication authority.

Human feedback is free-form text under `Review Thread`. For an actual feedback
entry, supply its next `Feedback NNN` heading and preserve the human's words and
language. No date, authority, target links or other metadata are mandatory.
Repeated feedback is legitimate. Never invent human acceptance or rewrite an
existing entry. After landing, append feedback only; a material analysis
correction follows the successor procedure in the note lifecycle.

Run the repository's available required validation. Report missing external
version 2 support as an implementation gap; do not create a fallback validator
or change the external implementation without explicit scope. Do not commit,
push or publish unless the operator requests that action.
