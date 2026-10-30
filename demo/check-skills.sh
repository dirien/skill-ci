#!/usr/bin/env bash
# Dataset-only check of the sample skills and the demo variants. No LLM calls, no live eval.
#   - SKILL.md has front matter whose name matches the skill
#   - evals/evals.json is valid JSON in the agentskills.io shape
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

need python3
status=0
check() {
  local dir="$1" skill="$2"
  python3 -I - "${dir}" "${skill}" <<'PY' || status=1
import json, re, sys
from pathlib import Path

d, skill = Path(sys.argv[1]), sys.argv[2]
problems = []
text = (d / "SKILL.md").read_text()
m = re.match(r"---\n(.*?)\n---\n", text, re.S)
if not m:
    problems.append("SKILL.md has no front matter")
elif not re.search(r"^name:\s*%s\s*$" % re.escape(skill), m.group(1), re.M):
    problems.append("front matter name is not %s" % skill)
data = json.loads((d / "evals" / "evals.json").read_text())
if data.get("skill_name") != skill:
    problems.append("skill_name is not %s" % skill)
ids = set()
for i, e in enumerate(data.get("evals", [])):
    for key in ("id", "prompt", "expected_output", "assertions", "expected_skill"):
        if not e.get(key):
            problems.append("evals[%d] misses %s" % (i, key))
    if e.get("id") in ids:
        problems.append("duplicate id %s" % e.get("id"))
    ids.add(e.get("id"))
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", str(e.get("id", ""))):
        problems.append("evals[%d] id is not Harbor-safe" % i)
if not data.get("evals"):
    problems.append("no evals")
label = "%s (%d evals)" % (d, len(data.get("evals", [])))
print(("FAIL " if problems else "ok   ") + label)
for p in problems:
    print("     - " + p)
sys.exit(1 if problems else 0)
PY
}

for dir in "${ROOT_DIR}"/skills/*/; do
  check "${dir%/}" "$(basename "${dir}")"
done
for dir in "${DEMO_DIR}"/variants/*/*/; do
  check "${dir%/}" "$(basename "$(dirname "${dir}")")"
done
exit "${status}"
