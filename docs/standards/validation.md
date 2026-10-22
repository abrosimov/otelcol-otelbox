---
note_id: "20260907191000"
created: 2026-09-07
type: meta
subtype: standard
gov_self_evolvement: false
retention: active
language: en
status: active
---
# Validation

The external `vault-tool` checks deterministic properties at synchronisation.
A claim that cannot be checked deterministically remains prose or a separately
labelled advisory; it does not become a hard rule by implication.

The active profile comes from `.vault-profile`. A nested vault is selected only
by a repository-root `.vault-root` naming one immediate child. Profile selection
does not define project identity.

Both markers are UTF-8 one-line protocols with one terminating LF and no
surrounding whitespace. `.vault-profile` contains one profile name.
`.vault-root` contains one immediate child basename: not empty, `.`, `..`,
dot-prefixed, slash-containing, backslash-containing, or a symbolic link. The
repository root carries exactly one of the two markers. The selected child must
carry `.vault-profile`; a child marker without the parent declaration never
retargets an operation. Deep markers and directory search provide no fallback.

The selected location must satisfy the effective `placement_policy`. A nested
marker names one of its `nested_child_preference` values. Initial materialisation
uses the first value whose immediate-child path does not exist; later validation
preserves any listed value. Initial root materialisation requires an explicit
caller selection and `allow_explicit_root_vault: true`; later validation requires
the resolved `.` path in the receipt. In any generated vault, the resolved
repository-relative path must equal the receipt value.

For the source vault, the marker value must name one
`validation_profiles` entry in the source distribution manifest. A generated
vault uses its generated `.cadmus-manifest.json`, which contains only the
selected installed projection. The receipt names the same profile but does not
carry or redefine its declaration. Required and forbidden root directories come
from that declaration. An unknown, missing, duplicate, or receipt-mismatched
profile is an error; implementations do not attach private rules to the strings
`scaffold` and `instance`.

Retention roots are not repeated in the profile. They come from
`config.retention_projection` in the effective vault schema and are required
independently of profile directory policy.

The assembled root schema declares the note population. Exact root service
directories and exact file basenames outside that population are data in
`config.validation_scope`; an implementation does not infer exclusions from
capitalisation, file extension, or a private hard-coded list.

Hard checks include required and forbidden directories, frontmatter key syntax,
retention-to-path agreement, portable Obsidian configuration, instruction-file
shape, landed-body immutability, Katoptron structure, and reference closure.
Wikilinks resolve by declared vault semantics; relative Markdown links resolve
from their source file. A link to an unavailable historical object uses an
explicit Git or external locator.

The `instance` profile permits descendant-owned content topology; it does not
invent one. This contract activates no community-plugin or graph-consumer check:
it defines no machine carrier for those descendant declarations. A future
extension must define that carrier before such checks become normative. The
profile marker alone names no plugin, content directory, or application policy.

The universal schema defines the shape of `subtype` but not a closed value list.
The assembled `type {}` declarations are its vocabulary authority, including
descendant-owned deltas. A validator that checks both a second skeleton enum and
the declarations implements a different, non-extensible contract.

The exact output selected by [[distribution]] is validated before an external
materialiser writes.
First-party distributed text is English, source identity is absent, and every
distributed skill, template, schema declaration, and normative standard has its
required peers.

The template carries the declarations and no validator implementation,
implementation test suite, language toolchain, or CI wrapper. `vault-tool` owns
executable conformance and must not redefine the schema. Validation detects; it never
applies a repair automatically. Passing checks against a synthetic substitute is
not proof that the real distribution is deployable.

Cross-note prose similarity, copy detection, and self-citation analysis are
future surfaces. This contract defines no normalisation, threshold, attribution,
or finding severity for them, so an implementation must not invent a hard gate.
