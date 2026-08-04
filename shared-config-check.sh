#!/usr/bin/env bash
# Fails when the SHARED regions of the three role profiles are not identical.
#
# The role profiles used to share a base layer that every deployment loaded
# alongside its own. That made the common settings identical by construction but
# obliged every consumer to know confmap's merge semantics and to vendor a copy
# of the base file. The layering is gone; this check is what replaces it, and it
# is stricter than the layering was — a role layer could always redefine a base
# key and win silently, whereas a divergence here fails the build.
#
# Comparison is byte-for-byte, comments included. That is deliberate: the blocks
# are meant to be one text in three files, so reformatting one of them is
# precisely the drift worth catching, and the fix is a copy and paste. It needs
# no YAML parser and therefore no dependency this repository does not already
# have.
#
#   ./shared-config-check.sh [config/edge.yaml config/gateway.yaml ...]

set -euo pipefail

open_marker='# >>> SHARED'
close_marker='# <<< SHARED'

# Each profile is expected to carry this many regions: the processor block, the
# service::telemetry::logs block and the service::telemetry::metrics::level
# block. Pinned rather than merely balanced, so that deleting a whole region
# from every file at once still fails instead of quietly comparing less.
expected_regions=3

configs=("$@")
if [ "${#configs[@]}" -eq 0 ]; then
    configs=(config/edge.yaml config/gateway.yaml config/host-agent.yaml)
fi

if [ "${#configs[@]}" -lt 2 ]; then
    echo "shared-config-check: need at least two profiles to compare, got ${#configs[@]}" >&2
    exit 2
fi

# An explicit template rather than a bare `mktemp -d`: BSD mktemp resolves the
# latter through confstr and ignores TMPDIR, which breaks in any sandbox that
# grants a temporary directory of its own.
workdir=$(mktemp -d "${TMPDIR:-/tmp}/shared-config-check.XXXXXX")
trap 'rm -rf "$workdir"' EXIT

status=0
reference=""
reference_file=""
reference_extract=""
config_index=0

for config in "${configs[@]}"; do
    if [ ! -f "$config" ]; then
        echo "shared-config-check: no such profile: $config" >&2
        exit 2
    fi

    opens=$(grep -F -c -- "$open_marker" "$config" || true)
    closes=$(grep -F -c -- "$close_marker" "$config" || true)

    # An unbalanced or missing marker must fail loudly. Left to the extraction
    # below it would silently yield a short region, or none at all, and three
    # empty regions compare equal — a check that passes because it compared
    # nothing is worse than no check.
    if [ "$opens" -ne "$expected_regions" ] || [ "$closes" -ne "$expected_regions" ]; then
        echo "shared-config-check: $config has $opens opening and $closes closing markers, expected $expected_regions of each" >&2
        echo "  a marker was renamed, deleted or duplicated — restore it before comparing" >&2
        exit 2
    fi

    extracted="$workdir/$config_index.shared"
    config_index=$((config_index + 1))
    # `close` is a reserved awk function name, hence the suffixed variables.
    if ! awk -v openm="$open_marker" -v closem="$close_marker" '
        index($0, openm) {
            if (inblock) exit 2
            inblock = 1
            region++
            print openm, region
            next
        }
        index($0, closem) {
            if (!inblock) exit 2
            inblock = 0
            print closem, region
            next
        }
        inblock { print }
        END { if (inblock) exit 2 }
    ' "$config" > "$extracted"; then
        echo "shared-config-check: $config has nested or out-of-order shared markers" >&2
        exit 2
    fi

    if [ ! -s "$extracted" ]; then
        echo "shared-config-check: $config yielded an empty shared region" >&2
        exit 2
    fi

    digest=$(shasum -a 256 < "$extracted" | cut -d' ' -f1)

    if [ -z "$reference" ]; then
        reference="$digest"
        reference_file="$config"
        reference_extract="$extracted"
        echo "shared-config-check: $config  $digest  (reference, $(wc -l < "$extracted" | tr -d ' ') lines)"
        continue
    fi

    if [ "$digest" = "$reference" ]; then
        echo "shared-config-check: $config  $digest  ok"
    else
        echo "shared-config-check: $config  $digest  DIVERGED from $reference_file" >&2
        diff -u "$reference_extract" "$extracted" >&2 || true
        status=1
    fi
done

if [ "$status" -ne 0 ]; then
    echo >&2
    echo "shared-config-check: the shared regions are meant to be one text in every profile." >&2
    echo "  Copy the reference region over the diverged one; do not reconcile them by hand." >&2
    exit 1
fi

echo "shared-config-check: ${#configs[@]} profiles agree on $expected_regions shared regions"
