# otelcol-otelbox documentation vault

This vault belongs to **otelcol-otelbox** and uses Kadmos distribution
`0.2.0` for its portable Obsidian profile, note contracts,
templates, and agent workflows.

Kadmos is a dependency of this vault, not the identity of the containing
project. The external materialiser records the installed distribution version,
validation profile, project identity, and managed-file hashes in its receipt.
The generated `.cadmus-manifest.json` is the installed machine authority for
profile, placement, expected files, and capability declarations; the receipt
does not redefine them.

The vault profile is declared in `.vault-profile`. If this vault is nested in a
larger repository, that repository selects it with a root `.vault-root` file.

Normative authoring and validation declarations live in `standards/`. Templates
live in `templates/`; agent workflows live in `.agents/skills/`. Project-owned
agent instructions may be added in `PROJECT_AGENTS.md` without changing the
managed base contract.

Validate with the external validator Lynkeus (`lynkeus-kadmou`). This vault does not carry a validator implementation, language toolchain, or CI wrapper.
