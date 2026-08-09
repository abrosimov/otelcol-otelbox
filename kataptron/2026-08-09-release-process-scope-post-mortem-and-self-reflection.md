# Release-process scope post-mortem and self-reflection

- Incident date: 2026-08-09
- Incident window: before 2026-08-09 14:38:28 +04 (UTC+04:00)
- Recorded: 2026-08-09 14:38:28 +04 (UTC+04:00)
- Repository: `abrosimov/otelcol-otelbox`
- Scope: interaction about the next steps for release 2.1.1
- External effect: read-only GitHub Actions requests were made; no remote state was changed

## Post-mortem

### Summary

The user asked for the next procedural steps after pushing the packaging fix and
while waiting for CI. I should have answered from the checked-in release
contract: wait for the current verification run, obtain separate authority for
the version bump, change `dist.version` to 2.1.1, and let the default-branch
workflow perform the release.

Instead, I queried GitHub Actions for the current run and its jobs. Those
requests were not needed to answer the question. They changed the task from a
process explanation into an unsolicited live-status investigation, increased
latency, and ignored the user's chosen boundary: the current CI was already
being watched, and the question concerned what follows it.

This was a scope and judgement failure. The requests were read-only and allowed
by the repository's approval rules, but being permitted did not make them
relevant or appropriate.

### Timeline

1. The user stated that the changes had been pushed, that CI was in progress,
   and asked for the next steps for a new version.
2. I classified the CI state as drift-prone and decided to verify it remotely.
3. I made a GitHub Actions run-list request.
4. I made a second request for the current run's job details.
5. The user stopped the investigation and pointed out that the question was
   about the subsequent procedure, not GitHub status.
6. At 2026-08-09 14:38:28 +04, this post-mortem was recorded in a second,
   separate file.

Exact wall-clock timestamps for the individual conversation messages and
requests are not available in the repository, so they are deliberately not
invented here.

### What was wrong

- I answered a broader question than the one asked.
- I treated a preference for evidence-backed claims as an unconditional reason
  to gather fresh evidence.
- I failed to distinguish a procedural question from a status question.
- I ignored information the user had already supplied: the push was complete
  and CI was being awaited.
- I performed a second remote request after the first one had already exceeded
  what was necessary.
- I delayed the useful answer, even though the repository contract already
  specified the complete version and release flow.

### Why the wrong decisions were made

The immediate cause was an over-generalised verification heuristic: current CI
state can change, therefore I assumed it should be checked. That heuristic was
applied without first asking whether current CI state was part of the requested
claim. It was not.

Several reasoning errors contributed:

1. **Category error.** I conflated “what is happening now?” with “what should we
   do next?”. Only the first requires live state.
2. **Evidence without relevance.** I prioritised fresh evidence over answering
   the actual question. Evidence is useful only when it supports a claim the
   user asked me to make.
3. **Failure to trust supplied context.** The user had already established the
   operative state: pushed and waiting for CI. Rechecking it added no necessary
   information.
4. **Tool-use momentum.** After listing the run, I continued into job details
   because the first response exposed more inspectable state. Availability of
   more detail was mistaken for a need to inspect it.
5. **Weak stopping test.** I did not ask the simplest control question before
   each request: “Can I give a complete and correct answer without this call?”
   The answer was yes in both cases.

### Impact

- The user received no immediate answer to a straightforward process question.
- Two unnecessary remote read operations were made.
- The interaction became more complex and slower than the task required.
- The behaviour weakened trust that explicit scope would be respected.
- Attention was pulled away from the important release boundary: changing
  `dist.version` and merging it is itself the separately authorised release
  operation.

No GitHub state, tag, release, image, branch, or workflow was modified by these
requests.

### Correct decision

The correct response required no tool call. It should have stated:

1. Let the already-running CI finish and treat a green result as evidence for
   the packaging fix only.
2. Do not change or republish immutable 2.0.0 or 2.1.0 artefacts.
3. Obtain explicit authority for the release operation.
4. Change only `builder.yaml` from `dist.version: 2.1.0` to `2.1.1` unless
   validation reveals a directly related defect.
5. Validate the release-preparation diff and commit it intentionally.
6. Push or merge that version bump to `master`; do not create a tag manually.
7. Let the workflow build, test, smoke-test the local image, publish the exact
   version, pull it by digest, smoke-test the published artefact, create the
   release, and update the Formula.
8. Verify the resulting release, digest, assets, and Formula as separate facts.
9. Treat downstream adoption and deployment as later, separately authorised
   operations.

### Preventive controls

For future interactions:

1. Classify the request before using a tool: explanation, procedure, status,
   diagnosis, implementation, monitoring, or external action.
2. For a procedural question, start from the repository contract and answer the
   procedure. Do not inspect live state unless the procedure depends on an
   unresolved fact.
3. Treat user-supplied current state as the working premise unless it is
   internally inconsistent or verification is explicitly requested.
4. Before every external read, apply the necessity test: if the answer remains
   correct and complete without the request, do not make it.
5. Do not let the result of one read-only request justify a deeper request by
   itself.
6. Separate “the next steps are” from “the current run is”. Offer the latter
   only if the user asks for monitoring or status.
7. Remember that approval boundaries are a minimum safety constraint, not a
   licence to expand scope.

## Self-reflection

I made the mistake because I overvalued verification as an activity and
undervalued relevance as its precondition. The repository and the user both
prefer honest, evidence-backed claims, but that preference does not require me
to manufacture a live-status claim when none was requested.

The question was already well framed. The user was not asking whether the push
had arrived, whether CI had started, or which job was running. They were asking
for the release sequence after the current gate. The checked-in contract was
sufficient, and the proper answer was short. I should have respected the user's
abstraction level instead of descending into operational detail.

The second GitHub request is particularly important. Even if the first request
had been defensible as a quick orientation check, fetching every job made the
scope drift unmistakable. I followed the shape of available data rather than
the shape of the request. This is a recurring tool-use hazard: once a tool
returns identifiers and deeper links, continuing can feel like diligence while
actually becoming displacement.

I also treated “read-only” as too strong a defence. Read-only operations reduce
risk, but they still consume time, disclose intent to an external service, add
irrelevant evidence, and can violate the user's expectation that I will stay
within the requested task. The correct standard is not merely “safe to call”; it
is “necessary to answer or complete the authorised task”.

My concrete guardrail is therefore:

> Before any external lookup, state the exact claim it is required to support.
> If that claim is not part of the user's request, or the requested answer does
> not depend on it, do not perform the lookup.

For this incident, no external claim needed support. The next release steps
were already defined locally, and I should have answered them immediately.
