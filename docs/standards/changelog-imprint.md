---
note_id: "20260910090100"
created: 2026-09-10
type: meta
subtype: standard
gov_self_evolvement: false
retention: active
language: en
status: active
---
# Changelog imprint

A changelog entry is a generated note-level imprint of one explicit Git
change-set that changes at least one governed note. It records only changed
notes under the dispositions `Added`, `Edited`, `Archived`, and `Buried`;
every item is a resolvable Wikilink and empty dispositions are omitted. It
contains no restatement, rationale, status summary, or manually authored
current-state claim.

Entries form a directory journal at `changelog/yyyy/mm/yyyy-mm-dd-slug.md`.
The heading is `# yyyy-mm-dd — slug`. Each non-empty disposition occupies one
line whose value is a comma-separated, deterministically ordered list of
Wikilinks:

```markdown
# 2026-09-07 — explicit-distribution

Added: [[standards/distribution]]
Edited: [[standards/validation]]
```

Each link is the candidate vault-root-relative path without `.md`. Links within
a disposition are sorted by their UTF-8 path bytes. Dispositions use the order
`Added`, `Edited`, `Archived`, `Buried`.

One qualifying change-set produces one entry. Derivation receives the exact base
and candidate Git revisions, an unclaimed schema-conforming `note_id`, a lowercase
ASCII kebab-case slug, and the effective vault schema. The candidate committer
timestamp in UTC supplies the date. The resulting frontmatter key order and
values are:

```yaml
---
note_id: "<explicit input>"
created: yyyy-mm-dd
type: meta
subtype: changelog-entry
retention: active
language: en
status: immutable
---
```

When the effective schema requires `gov_self_evolvement`, the generator inserts
`gov_self_evolvement: true` between `subtype` and `retention`; when it is
optional, the field is omitted. An entry is immutable after landing and is
excluded from its own input.

Base and candidate notes correspond by `note_id`. A candidate with no base
identity is `Added`. An existing note whose candidate changes retention to
`archived` or `buried` is respectively `Archived` or `Buried`; these
classifications take precedence over rename and frontmatter changes. Any other
admitted frontmatter change, append-only body event admitted by
[[note-lifecycle]], or rename preserving `note_id` is `Edited`. Any other
landed-body change fails lifecycle validation before derivation. A base identity
with no candidate note is not silently classified; derivation refuses.

Only governed notes appear. Schema data, templates, skills, configuration,
vendor inputs, and the changelog entry itself are excluded. A change-set that
changes no governed note produces no entry.

Derivation belongs to the external Go implementation under [[tooling-boundary]].
It excludes the output entry from its own input and emits deterministic bytes.
Re-derivation over the same declared inputs must reproduce the accepted entry
exactly.
