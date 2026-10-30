#!/usr/bin/env bash
# Put the demo back to its start so it can run again:
#   - delete all SkillEvals, their Jobs and any trial Sandboxes left behind
#   - remove the beta/stable tags and every skill version except v1.0.0 from Harbor
#   - restore the model list from demo/models.yaml
# Skills, the Harbor project and the webhook policy stay.
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

need kubectl
need python3

say "Deleting SkillEvals and their Jobs"
if kubectl get crd skillevals.skillci.dev >/dev/null 2>&1; then
  kubectl -n "${NS}" delete skillevals --all --wait=false
else
  note "No SkillEval CRD yet, nothing to delete."
fi
kubectl -n "${NS}" delete jobs -l skillci.dev/skill --wait=false --ignore-not-found
# Owner references clean these up with the runner pods; this catches older runs.
kubectl -n "${NS}" delete sandboxes.agents.x-k8s.io -l app.kubernetes.io/managed-by=harbor --wait=false --ignore-not-found

wait_toolbox
say "Resetting tags in Harbor"
repos="$(harbor_api GET "/projects/${HARBOR_PROJECT}/repositories?page_size=100" |
  python3 -I -c 'import json,sys; [print(r["name"].split("/",1)[1]) for r in json.load(sys.stdin)]')"
while IFS= read -r repo; do
  [[ -n "${repo}" ]] || continue
  actions="$(harbor_api GET "/projects/${HARBOR_PROJECT}/repositories/${repo}/artifacts?with_tag=true&page_size=100" |
    python3 -I -c '
import json, sys
for art in json.load(sys.stdin):
    names = [t["name"] for t in (art.get("tags") or [])]
    if "v1.0.0" in names:
        for ch in ("beta", "stable"):
            if ch in names:
                print("untag", art["digest"], ch)
    elif names:
        print("delete", art["digest"], "-")
')"
  while read -r action digest tag; do
    [[ -n "${action}" ]] || continue
    case "${action}" in
    untag)
      note "${repo}: removing tag ${tag} from ${digest:0:19}..."
      harbor_api DELETE "/projects/${HARBOR_PROJECT}/repositories/${repo}/artifacts/${digest}/tags/${tag}" >/dev/null
      ;;
    delete)
      note "${repo}: deleting demo version ${digest:0:19}..."
      harbor_api DELETE "/projects/${HARBOR_PROJECT}/repositories/${repo}/artifacts/${digest}" >/dev/null
      ;;
    esac
  done <<<"${actions}"
done <<<"${repos}"

say "Restoring the model list"
"${DEMO_DIR}/model-swap.sh" restore

say "Reset done. Start again with: make seed RETRIGGER=1"
