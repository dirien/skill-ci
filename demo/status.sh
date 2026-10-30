#!/usr/bin/env bash
# Show what the pipeline has done: SkillEvals, then the tags and digests per skill in Harbor.
#   demo/status.sh              both
#   demo/status.sh --tags-only  only the Harbor part
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

need kubectl
need python3

if [[ "${1-}" != "--tags-only" ]]; then
  say "SkillEvals"
  if kubectl get crd skillevals.skillci.dev >/dev/null 2>&1; then
    kubectl -n "${NS}" get skillevals -o wide
  else
    note "The SkillEval CRD is not installed yet. Run 'make deploy'."
  fi
fi

wait_toolbox
say "Skills in Harbor (project ${HARBOR_PROJECT})"
repos="$(harbor_api GET "/projects/${HARBOR_PROJECT}/repositories?page_size=100" |
  python3 -I -c 'import json,sys; [print(r["name"].split("/",1)[1]) for r in json.load(sys.stdin)]')"
if [[ -z "${repos}" ]]; then
  note "No skills yet. Run 'make seed'."
  exit 0
fi
printf '    %-24s %-22s %s\n' SKILL DIGEST TAGS
while IFS= read -r repo; do
  harbor_api GET "/projects/${HARBOR_PROJECT}/repositories/${repo}/artifacts?with_tag=true&page_size=100" |
    python3 -I -c '
import json, sys
repo = sys.argv[1]
for art in sorted(json.load(sys.stdin), key=lambda a: a["push_time"]):
    tags = ", ".join(sorted(t["name"] for t in (art.get("tags") or []))) or "(untagged)"
    print("    %-24s %-22s %s" % (repo, art["digest"][:19], tags))
' "${repo}"
done <<<"${repos}"
