#!/usr/bin/env bash
# Remove one skill version from the demo: its SkillEvals and their Jobs, and its artifact in Harbor.
# Used after recording step 1, so the stage can push the same live version again.
#   demo/forget.sh k8s-incident-triage v1.1.1
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

need kubectl
[[ $# -eq 2 ]] || die "usage: forget.sh <skill> <version>"
skill="$1" version="$2"
case "${version}" in
v1.0.0) die "v1.0.0 is the base of the demo state; use make reset instead" ;;
esac

names="$(kubectl -n "${NS}" get skillevals -l "skillci.dev/skill=${skill}" \
  -o jsonpath="{range .items[?(@.spec.version==\"${version}\")]}{.metadata.name}{\"\n\"}{end}")"
while IFS= read -r name; do
  [[ -n "${name}" ]] || continue
  note "deleting SkillEval ${name} and its Jobs"
  kubectl -n "${NS}" delete skilleval "${name}" --wait=false
  kubectl -n "${NS}" delete jobs -l "job-name=${name}" --wait=false --ignore-not-found >/dev/null
  kubectl -n "${NS}" delete job "${name}" --wait=false --ignore-not-found >/dev/null
done <<<"${names}"

wait_toolbox
digest="$(tag_digest "${skill}" "${version}")"
if [[ -n "${digest}" ]]; then
  note "deleting ${skill}:${version} (${digest:0:19}...) from Harbor"
  harbor_api DELETE "/projects/${HARBOR_PROJECT}/repositories/${skill}/artifacts/${digest}" >/dev/null
else
  note "${skill}:${version} is not in Harbor"
fi
