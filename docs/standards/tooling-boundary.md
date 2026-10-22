---
note_id: "20260910090000"
created: 2026-09-10
type: meta
subtype: standard
gov_self_evolvement: false
retention: active
language: en
status: active
---
# Tooling boundary

The template publishes declarations: Markdown standards, schema data and its
protobuf shape, templates, skills, the distribution manifest, declarative
Obsidian configuration, and explicitly admitted capability data. It contains no
validator, materialiser, generator, installer, language toolchain,
implementation test suite, or CI wrapper.

Executable vault automation belongs to the external `vault-tool`. The template
states what that implementation must observe and report; it does not carry a
fallback renderer or a differential implementation. Missing
behaviour is reported as an implementation gap, not prototyped here. Inspecting
or changing the implementation repository requires that repository to be
explicitly included in the current task scope.

An external tool receives authority from its caller, not from schema metadata or
the distribution manifest. It resolves the vault boundary, validates before
writing, refuses path escape and collisions, and keeps detection separate from
mutation. See [[distribution]] and [[validation]].
