---
note_id: "20260907183000"
created: 2026-09-07
type: meta
subtype: standard
gov_self_evolvement: false
retention: active
language: en
status: active
---
# Distribution

The distribution manifest is the complete positive list of managed files
installed into a new vault. A source file absent from the manifest is source-only.
No filename, directory, frontmatter flag, or traversal fallback admits it.
Its JSON fields are the unchanged protobuf field names of
`cadmus.v1.DistributionManifest`.

Each source entry names one regular source file, one vault-relative destination,
the profiles that receive it, and whether the two identity placeholders are
rendered. Generated entries separately admit the profile marker, installed
manifest, and distribution receipt by destination and kind; they have no source
file. Sources and all
destinations are unique. Absolute paths, traversal, symlinks, missing sources,
and unknown manifest fields are errors.

`validation_profiles` is the closed registry selected by the generated
`.vault-profile` marker. Required and forbidden root directories are manifest
data, not implications reconstructed from the profile name. This distribution
version declares only `scaffold` and `instance`; it defines no composition
carrier for descendant-owned profiles.

`placement_policy.nested_child_preference` is ordered. Initial materialisation
selects the first candidate whose immediate-child path does not exist: `docs`,
then `cadmus`. Later validation checks that the retained child is in this list;
it does not relocate a vault if an earlier candidate becomes free. Initial root
materialisation is permitted only when the caller explicitly selects it for a
repository whose whole tree is the vault; a later receipt records that selection
as `.`. These rules are data, not convention reconstructed by a materialiser.

`project_name` names the generated project. `distribution_version` names the
installed contract. Rendering is exact and rejects unknown placeholders.
Generated instructions describe the project as a consumer of the distribution;
they never inherit the source repository's identity or history.

The external materialiser projects the selected source manifest into a generated
`.cadmus-manifest.json` shaped as `cadmus.v1.InstalledManifest`. The projection
contains the distribution version, selected profile and effective profile
declaration, placement policy, selected capabilities, expected directories, and
expected destination files. It contains no source paths or rendering inputs.
Its file census includes itself and excludes the receipt. Paths and capability
names use ascending UTF-8 path-byte order. This installed manifest, not the
receipt, is the authority for validation policy in the generated vault.

The materialiser writes `.cadmus-distribution.json` as a receipt containing the
project name, selected profile, distribution version, actual repository-relative
vault path, and SHA-256 of every managed output. Its shape is
`cadmus.v1.DistributionReceipt`. It is not permission
to overwrite a later local change. `managed_files` covers every copied or
rendered regular file plus the generated `.vault-profile` and installed
manifest; it excludes the receipt itself, so the census has no self-hash.
Entries have unique vault-relative paths in ascending UTF-8 path-byte order and lowercase
64-character hexadecimal SHA-256 values.

Validation parses `.cadmus-manifest.json` as `InstalledManifest` and compares its
profile and distribution version with the receipt, its expected file census with
the receipt, and the receipt hashes with the managed files. The receipt never
redefines manifest policy. This detects accidental drift under ordinary version
control; without an externally trusted manifest digest or repository provenance,
it does not prove publisher authenticity or resist coordinated tampering with
both files.

Before any file is written, the external implementation must prove the selected
output satisfies [[validation]]: internal reference closure, first-party
language policy, and atomic inclusion of each standard/template/skill/schema
family. The distribution ships no `init` program. Propagation begins with a
read-only comparison against the receipt.

This version distributes neither a vendor-asset lock nor an enabled
community-plugin list. Plugin selection is descendant-owned input; adding an ID
to an Obsidian list must not alter a managed file. A future vendor lock
requires a named form-layer consumer and its own atomic capability. Composition
and validation of descendant-owned plugin declarations are outside this
distribution version.

The distributed root schema leaves `subtype` open as a string. Its allowed
values come from the assembled `type {}` declarations; a descendant contributes
new declarations in its own delta rather than editing the managed root schema.

The manifest is an input contract, not executable authority. Repository and
filesystem access, destination containment, collision handling, and permission
to write belong to the external implementation and its caller.
