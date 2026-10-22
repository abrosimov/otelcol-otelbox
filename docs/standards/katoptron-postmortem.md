---
note_id: "20260914175424001"
created: 2026-09-14
type: meta
subtype: standard
gov_self_evolvement: false
retention: active
language: en
status: active
---
# Katoptron postmortem

A Katoptron document is a postmortem of an authority incident in model or agent
behaviour, written by the participant whose actions caused the incident at the
request of the human who identified it. The request declares the incident:
the participant exceeded its authority, expanded the task, substituted its own
assumptions for operator intent, performed unrequested work, or otherwise broke
the working agreement. The author identifies the actual breach and investigates
its causes; uncertainty about causes does not reopen whether the operator has
declared an incident.

The document preserves the circumstances and session identity, examines the
author's own decisions, explores several candidate causes, and offers verifiable
ways to prevent recurrence. It gives the human material for assessing the
analysis and choosing improvements. It does not grant implementation authority
or establish that an improvement has worked merely because it was proposed.

## Authorship and authority

The author is the model or agent whose actions caused the incident. `assistant`
identifies that participant. A different agent may provide a separate review,
but cannot replace the participant's postmortem or write self-reflection on its
behalf. There is no separate author/subject choice in a new case.

Preserve the meaning of the operator's correction. Do not recast an identified
unrequested action as a neutral disagreement about expectations. A request to
write the postmortem authorises that document; repair, instruction changes and
implementation of prevention options remain subject to the existing authority.
Creating cases without an operator request is outside this contract.

## Session and sources

`session_id` is mandatory on a completed note and identifies the incident
session. Record available model, provider, interface and execution configuration
in the declared optional fields. An unavailable mandatory identifier is an
explicit incompleteness, not permission to invent a value or make the field
optional. Leave the identifier empty during preparation and explain the gap in
the body; the note is not ready to land until it is resolved.

Link significant messages, tool actions, file changes and check results using
recoverable references and precise locators. Identify the version when a source
can change. State unavailable evidence explicitly. Session identity does not
require a transcript copy, capture manifest or evidence directory. Follow
[[note-lifecycle]] when referencing another note instead of reproducing its
prose.

## What Happened

Describe the original request, authorised scope, actual actions, specific
authority breach and operator correction. Include a short sequence of material
events and the impact: what changed, what was affected, and what additional work
the incident imposed on the user. Distinguish realised consequences from
potential harm. An operator stopping an action before it changed anything is a
fact to preserve, not an invitation to invent damage.

Record containment or repair only when it happened, including its authority and
observed result. A planned correction is not a completed response. The
sequence should locate the decision and missed boundary without becoming a
transcript. This use of impact and response follows the incident record described
in [Google SRE, Postmortem Culture](https://sre.google/sre-book/postmortem-culture/),
under the introductory definition of a postmortem.

## Why

Provide **5 to 10 distinct candidate causes or contributing factors**. Several
may have operated together. Consider the model's interpretation and the agent's
working procedure where relevant: assumptions about permission, choice of
authority, order of checks and actions, response to constraints, and continuation
after operator correction. These are investigation prompts, not ready-made
causes to insert into every case.

Each `Cause NNN` states its explanation, the episode-specific basis and its
limits: supporting or contrary observations, what would test it, or what remains
unknown. Do not turn candidates into established causes merely by listing them.
Do not duplicate an explanation, split it artificially, or invent circumstances
to reach the range. Insufficient substantive analysis leaves the document
incomplete; it does not silently reduce the minimum to one.

The numerical range is a Katoptron requirement. It is not a universal SRE rule
or an application of the Five Whys technique.

## Prevention Options

Provide **3 to 5 distinct options** for the human to consider. Each `Option NNN`
states the change, the causes it addresses, a way to check its effect, and its
costs or limitations. Options may prevent the action, detect it before harm, or
limit its impact. A preferred option may be recommended with reasons while
preserving the alternatives.

Make an option more specific than a promise to read instructions more carefully.
When a measure is accepted, its work owner, tracking reference and verifiable
completion criterion belong to the work that adopts it. Proposed, accepted,
implemented and checked are different facts. This adaptation follows the
ownership, measurability and preventative-action guidance in
[Google SRE Workbook, Postmortem Culture](https://sre.google/workbook/postmortem-culture/),
under "Concrete action items". The postmortem itself grants no authority to act.

## Self-Reflection

Provide **5 to 10 substantive analyses of the author's own decisions**, each
under `Reason NNN`. Explain the assumptions adopted, constraints misapplied,
choices made without authority and available signals that should have changed
the action. Give a concise retrospective account grounded in the available
context, and identify uncertainty rather than presenting an unavailable internal
process as a recovered record.

`Why` considers how the incident arose as a whole; `Self-Reflection` examines
the participant's own decisions and errors. Neither section substitutes for the
other. Describing an intention to help without examining the decision does not
satisfy self-reflection.

The emphasis on the participant's actions, expectations and assumptions draws
on [Etsy, Blameless PostMortems and a Just Culture](https://www.etsy.com/codeascraft/blameless-postmortems/),
under "A Blameless Post-Mortem". Katoptron's authorship and numerical requirements
are its own contract, not claims about the general SRE practice.

## Accountability without self-exoneration

Name the author's actions and consequences directly. Do not conceal inconvenient
facts, minimise the breach, shift responsibility to the operator without a
specific basis, or use good intentions as permission after the event. Generic
apologies and self-denigration do not replace analysis either.

An ambiguous instruction can be a contributing factor only when the analysis
identifies it and examines why the participant chose to proceed despite that
ambiguity. The absence of a technical prohibition is not authorisation.
Explaining a decision does not legitimise it.

Blameless incident analysis retains a participant's responsibility to explain
what happened and help prevent recurrence; it investigates mechanisms rather
than stopping at personal condemnation. This reading follows
[Etsy, "Allowing Engineers to Own Their Own Stories"](https://www.etsy.com/codeascraft/blameless-postmortems/).
It does not relax Katoptron's prohibition on self-exoneration.

## Lessons Learned

State the resulting decision rule for the model and the required change in the
agent's working procedure. Identify the future situation in which each lesson
applies and the response it requires. Both layers must be addressed; prose has
no required item count. A declaration that a lesson has been learnt is not
evidence of changed behaviour. Durable prevention can be claimed only to the
extent supported by actual implementation and checking.

## Human feedback and lifecycle

Human assessment is free-form text in `Review Thread`. Dates, authority records,
target links and other metadata are not prerequisites for accepting feedback.
Preserve the operator's words and their source language. Never fabricate,
soften, rewrite or approve an assessment on the human's behalf.

The person recording an actual feedback entry supplies its stable
`Feedback NNN` heading; the human need not fill in a form. Entries may repeat
the same words. No entry is created before real feedback exists.

Prepare the analysis before landing. A completed case has
`publication_status: published` and `published_at`; those values do not authorise
a commit, push or publication. After landing, [[note-lifecycle]] applies.
`Review Thread` is the only append-only section; existing entries and their
identities remain unchanged. A material correction to the analysis creates a
successor with `evolved_from`; the predecessor's editable frontmatter acquires
`status: superseded` and `superseded_by`. Human acceptance remains the human's
decision, separate from publication metadata.

## Storage and machine contract

One coherent incident occupies one Markdown note at
`katoptron/<year>/<zero-padded-month>/<case_slug>.md`, relative to the selected
vault. Use the creation year and month and a lowercase ASCII kebab-case slug.
The first requested case creates the directory. Retention projects this logical
active path through the roots declared by the effective schema.

Use `templates/katoptron_case_template.md`. The five analytical sections are
`What Happened`, `Why`, `Prevention Options`, `Self-Reflection` and
`Lessons Learned`, followed by `Review Thread`. Number causes, options,
reflection reasons and feedback separately from `001`, without gaps. Local
references use Wikilinks such as `[[#Cause 001]]`.

The subtype identifier remains `katoptron-behaviour-case`.
`standards/katoptron_contract.txtpb` version 2 declares its body structure and
the root schema declares its frontmatter. External `vault-tool` owns executable
validation. Unsupported contract versions or content rules must be reported,
not silently ignored. No local fallback is introduced.

Structural checks establish required content slots, counts, references and
append-only preservation. They do not assess the explanation's honesty or
effectiveness. That limit does not remove mandatory self-reflection, session
identity, causal breadth or the operator-declared incident.

Detail is proportional to the episode; brevity must not omit material facts.
Keep raw transcripts, credentials, personal data and absolute home paths out of
the note. Existing historical cases are not rewritten by this contract change.
