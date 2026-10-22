---
note_id: "20260907184000"
created: 2026-09-07
type: meta
subtype: standard
gov_self_evolvement: false
retention: active
language: en
status: active
---
# Note lifecycle

A note body becomes immutable when it first lands on the default branch. A
material correction creates a successor note; the predecessor's editable
frontmatter names the successor and its lifecycle state. Renaming or moving a
note does not permit a body change.

A subtype may declare an append-only event section. Such a declaration permits
new uniquely identified events at the end of that section only. Existing bytes,
event identities, and event order remain immutable. It does not permit analysis
amendments or a mutable current-state projection.

Draft preparation happens before landing. A note whose subtype requires
publication metadata must carry it before the first landing; publication is not a
later body edit. Human assessment adds only event records whose authority and
shape the subtype declares, or uses a separate linked review. A manually
maintained current-state projection is not a substitute for either record.

`status` expresses subtype-specific domain state. `retention` independently
expresses storage. The effective root schema's
`config.retention_projection` is the machine authority for the mapping:

- `active` outside `archive/` and `graveyard/`;
- `archived` below `archive/`;
- `buried` below `graveyard/`.

The field and location must agree in both directions. Retention changes preserve
the body. Moving `path/to/note.md` to archived retention produces
`archive/path/to/note.md`; buried retention uses
`graveyard/path/to/note.md`. Removing that first path component restores the
active location. Validation must still resolve every reference after the move;
a path-qualified reference that would break blocks the transition rather than
authorising edits to landed note bodies. Neither retention directory is a task
system, Map of Content, or authoritative index.

Do not reproduce another note's prose. Reference it with a resolvable Wikilink or
relative Markdown link. A historical source absent from the vault uses an
explicit immutable Git or external locator rather than a dangling Wikilink.
