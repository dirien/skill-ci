#!/usr/bin/env bash
# Build the run report: one self-contained HTML page with every SkillEval, the
# channel tags in Harbor and SkillEvaluator's own reports inlined.
#   demo/report-page.sh [out-file]      (default reports/index.html)
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

out="${1:-reports/index.html}"
need kubectl
need python3

work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

say "Reading SkillEvals from the cluster"
kubectl -n "${NS}" get skillevals -o json >"${work}/skillevals.json"

wait_toolbox
say "Reading channel tags from Harbor (project ${HARBOR_PROJECT})"
repos="$(harbor_api GET "/projects/${HARBOR_PROJECT}/repositories?page_size=100" |
  python3 -I -c 'import json,sys; [print(r["name"].split("/",1)[1]) for r in json.load(sys.stdin)]')"
: >"${work}/tags.jsonl"
while IFS= read -r repo; do
  [[ -n "${repo}" ]] || continue
  harbor_api GET "/projects/${HARBOR_PROJECT}/repositories/${repo}/artifacts?with_tag=true&page_size=100" |
    python3 -I -c '
import json, sys
for art in json.load(sys.stdin):
    tags = [t["name"] for t in (art.get("tags") or [])]
    if tags:
        print(json.dumps({"skill": sys.argv[1], "digest": art["digest"], "tags": tags}))
' "${repo}" >>"${work}/tags.jsonl"
done <<<"${repos}"
python3 -I -c 'import json,sys; print(json.dumps([json.loads(l) for l in open(sys.argv[1]) if l.strip()]))' \
  "${work}/tags.jsonl" >"${work}/tags.json"

say "Pulling SkillEvaluator reports for every version that has one"
python3 -I -c '
import json, sys
seen = set()
for item in json.load(open(sys.argv[1]))["items"]:
    if item.get("status", {}).get("reportDigest"):
        key = (item["spec"]["skill"], item["spec"]["version"])
        if key not in seen:
            seen.add(key)
            print(*key)
' "${work}/skillevals.json" | while read -r skill version; do
  "${ROOT_DIR}/demo/report.sh" "${skill}" "${version}" "" "${work}/reports" >/dev/null
  note "${skill} ${version}"
done

say "Rendering ${out}"
python3 -I "${ROOT_DIR}/tools/report_page.py" \
  --skillevals "${work}/skillevals.json" --tags "${work}/tags.json" \
  --reports "${work}/reports" --out "${out}"
note "Open it in a browser: ${out}"
