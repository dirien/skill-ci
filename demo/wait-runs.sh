#!/usr/bin/env bash
# Wait until every SkillEval of a skill version has finished (Succeeded or Failed), then return.
# Used off camera by the recordings in demo/recordings/, and handy before a "one I prepared earlier".
#   demo/wait-runs.sh <skill> <version> [agent-version]
#   TIMEOUT=1800 demo/wait-runs.sh k8s-incident-triage v1.2.0
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

need kubectl
[[ $# -ge 2 ]] || die "usage: wait-runs.sh <skill> <version> [agent-version]"
skill="$1" version="$2" agent_version="${3-}"
selector="skillci.dev/skill=${skill}"
[[ -z "${agent_version}" ]] || selector+=",skillci.dev/agent-version=${agent_version}"
deadline=$((SECONDS + ${TIMEOUT:-1800}))

while :; do
  phases="$(kubectl -n "${NS}" get skillevals -l "${selector}" \
    -o jsonpath="{range .items[?(@.spec.version==\"${version}\")]}{.status.phase}{\"\n\"}{end}")"
  total="$(grep -c . <<<"${phases}" || true)"
  open="$(grep -c -v -E '^(Succeeded|Failed)$' <<<"${phases}" || true)"
  if [[ "${total}" -gt 0 && "${open}" -eq 0 ]]; then
    exit 0
  fi
  [[ "${SECONDS}" -lt "${deadline}" ]] || die "${skill} ${version}: ${open} of ${total} runs still open"
  sleep 10
done
