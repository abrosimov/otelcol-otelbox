#!/usr/bin/env bash
# Post-build smoke check: does the binary contain what builder.yaml declares?
#
# Per-kind counts rather than names, because a component reports its type, not
# its module path (resourcedetectionprocessor → resource_detection), and a
# hand-maintained translation table would rot on every bump. file_storage and
# redaction are additionally asserted by name — they are the invariants the
# collector exists for.
#
# Usage: ./smoke-check.sh <binary> [builder.yaml]

set -euo pipefail

readonly KINDS="receivers processors exporters extensions connectors"

bin="${1:-}"
manifest="${2:-builder.yaml}"

if [[ -z "${bin}" ]]; then
    echo "usage: $0 <binary> [builder.yaml]" >&2
    exit 64
fi
if [[ ! -x "${bin}" ]]; then
    echo "smoke-check: '${bin}' is not an executable" >&2
    exit 66
fi
if [[ ! -f "${manifest}" ]]; then
    echo "smoke-check: '${manifest}' not found" >&2
    exit 66
fi

# Seeds every counter to 0 so a missing section reports as 0 rather than as
# absent — the binary prints `connectors: []` when none are linked.
_count() {
    awk -v kinds="${KINDS}" -v entry="$1" '
        BEGIN {
            split(kinds, k, " ")
            for (i in k) n[k[i]] = 0
        }
        /^[a-z_]+:/ {
            section = $1
            sub(/:.*/, "", section)
            next
        }
        $0 ~ entry {
            if (section in n) n[section]++
        }
        END {
            split(kinds, k, " ")
            for (i = 1; i <= length(k); i++) printf "%s %d\n", k[i], n[k[i]]
        }
    '
}

declared="$(_count '^[[:space:]]*-[[:space:]]*gomod:' <"${manifest}")"
linked="$("${bin}" components | _count '^[[:space:]]*-[[:space:]]*name:')"

failed=0
for kind in ${KINDS}; do
    want="$(printf '%s\n' "${declared}" | awk -v k="${kind}" '$1 == k { print $2 }')"
    got="$(printf '%s\n' "${linked}" | awk -v k="${kind}" '$1 == k { print $2 }')"
    if [[ "${want}" != "${got}" ]]; then
        echo "smoke-check: ${kind}: builder.yaml declares ${want}, binary links ${got}" >&2
        failed=1
    else
        echo "smoke-check: ${kind}: ${got} ✓"
    fi
done

for required in file_storage redaction; do
    if ! "${bin}" components | grep -q "name: ${required}$"; then
        echo "smoke-check: required component '${required}' missing from the binary" >&2
        failed=1
    else
        echo "smoke-check: ${required} present ✓"
    fi
done

if [[ "${failed}" -ne 0 ]]; then
    echo "smoke-check: FAILED — the built binary does not match builder.yaml" >&2
    exit 1
fi

echo "smoke-check: OK"
