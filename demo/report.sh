#!/usr/bin/env bash
# List the eval reports attached to a skill version in Harbor (OCI referrers) and download them.
#   demo/report.sh <skill> <version> [model] [out-dir]
# Without a model the newest report of every agent@version/model entry is downloaded. The default out-dir is reports/.
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

[[ $# -ge 2 ]] || die "usage: report.sh <skill> <version> [model] [out-dir]"
skill="$1"
version="$2"
model="${3-}"
out="${4:-reports}"
report_type="application/vnd.skillci.report.v1+json"
[[ "${version}" =~ ${SEMVER_RE} ]] || die "version must look like v1.2.3, got '${version}'"

need kubectl
need tar
need python3
wait_toolbox

digest="$(tag_digest "${skill}" "${version}")"
[[ -n "${digest}" ]] || die "${skill}:${version} does not exist in Harbor"
subject="${HARBOR_HOST}/${HARBOR_PROJECT}/${skill}@${digest}"

say "Reports attached to ${skill}:${version} (${digest:0:19}...)"
# One line per report, newest first: digest, created, model, agent, agent version, lift.
rows="$(tb oras discover "${ORAS_FLAGS[@]}" --format json "${subject}" </dev/null |
  python3 -I -c '
import json, sys
tree = json.load(sys.stdin)
refs = tree.get("referrers") or tree.get("manifests") or []
refs = [r for r in refs if r.get("artifactType") == sys.argv[1]]
for r in sorted(refs, key=lambda r: r.get("annotations", {}).get("org.opencontainers.image.created", ""), reverse=True):
    a = r.get("annotations", {})
    print("\t".join([r["digest"], a.get("org.opencontainers.image.created", "-"),
                     a.get("skillci.dev/model", "-"), a.get("skillci.dev/agent", "-"),
                     a.get("skillci.dev/agent-version", "-"), a.get("skillci.dev/lift", "-")]))
' "${report_type}")"
if [[ -z "${rows}" ]]; then
  note "No reports yet. They are attached by runner images that support it."
  exit 0
fi

printf '    %-22s %-22s %-28s %-12s %-10s %s\n' REPORT CREATED MODEL AGENT AGENT-VER LIFT
picked=()
seen=" "
while IFS=$'\t' read -r rdigest created rmodel agent aversion lift; do
  printf '    %-22s %-22s %-28s %-12s %-10s %s\n' "${rdigest:0:19}" "${created}" "${rmodel}" "${agent}" "${aversion}" "${lift}"
  entry="${agent}@${aversion}/${rmodel}"
  # Skip other models than the one asked for, and older reports of an entry already picked.
  [[ -z "${model}" || "${rmodel}" == "${model}" ]] || continue
  [[ "${seen}" != *" ${entry} "* ]] || continue
  seen+="${entry} "
  picked+=("${rdigest}"$'\t'"${entry}")
done <<<"${rows}"
[[ ${#picked[@]} -gt 0 ]] || die "no report for model '${model}'"

for item in "${picked[@]}"; do
  rdigest="${item%%$'\t'*}"
  entry="${item#*$'\t'}"
  # The entry may contain / and : (ollama-cloud/gpt-oss:120b): one directory name.
  target="${out}/${skill}-${version}-${entry//[^A-Za-z0-9._@-]/_}"
  say "Downloading ${rdigest:0:19}... to ${target}/"
  # The pod's /workspace is scratch space; $1 is the directory, the rest are oras arguments.
  # shellcheck disable=SC2016  # expands in the toolbox shell, not here
  tb sh -c 'rm -rf "$1" && mkdir -p "$1" && cd "$1" && shift && oras pull "$@"' sh \
    "/workspace/report" "${ORAS_FLAGS[@]}" "${HARBOR_HOST}/${HARBOR_PROJECT}/${skill}@${rdigest}" </dev/null >/dev/null
  mkdir -p "${target}"
  tb tar -C /workspace/report -cf - . | tar -xf - -C "${target}"
  find "${target}" -type f | sed 's/^/    /'
done
