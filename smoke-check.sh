#!/usr/bin/env bash
# Post-build smoke check: does the binary contain what builder.yaml declares?
#
# Per-kind counts catch additions and removals, exact module/version pairs bind
# the binary to the manifest, and the canonical component names catch an alias
# or type swap inside a kind. All three are needed: none implies the other two.
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

components="$("${bin}" components)"
declared="$(_count '^[[:space:]]*-[[:space:]]*gomod:' <"${manifest}")"
linked="$(printf '%s\n' "${components}" | _count '^[[:space:]]*-[[:space:]]*name:')"

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

while read -r module version; do
    if ! awk -v module="${module}" -v version="${version}" '
        $1 == "module:" && $2 == module && $3 == version { found = 1 }
        END { exit !found }
    ' <<<"${components}"; then
        echo "smoke-check: declared module '${module} ${version}' missing from the binary" >&2
        failed=1
    fi
done < <(awk '$1 == "-" && $2 == "gomod:" { print $3, $4 }' "${manifest}")

for required in bearertokenauth file_storage filter headers_setter healthcheckv2 \
    host_metrics journald memory_limiter otlp otlp_grpc otlp_http prometheus \
    redaction resource_detection file; do
    if ! grep -q "name: ${required}$" <<<"${components}"; then
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
