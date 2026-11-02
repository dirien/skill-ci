#!/usr/bin/env bash
# The SkillEvals of one skill in a table that fits a stage terminal: version, agent and its release,
# model, scores with and without the skill, lift, its interval and the verdict. Oldest run first.
#   demo/results.sh [skill]    default k8s-incident-triage
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

need kubectl
need python3
skill="${1:-k8s-incident-triage}"

kubectl -n "${NS}" get skillevals -l "skillci.dev/skill=${skill}" \
  --sort-by=.metadata.creationTimestamp -o json |
  python3 -I -c '
import json, sys
rows = [("VERSION", "AGENT", "MODEL", "WITH", "W/O", "LIFT", "INTERVAL", "VERDICT")]
for e in json.load(sys.stdin)["items"]:
    sp, st = e["spec"], e.get("status", {})
    agent = sp["agent"] + ("@" + sp["agentVersion"] if sp.get("agentVersion") else "")
    model = sp["model"].rsplit("/", 1)[-1]
    verdict = st.get("verdict") or st.get("phase") or "Pending"
    rows.append((sp["version"], agent, model, st.get("withSkill", "-"), st.get("withoutSkill", "-"),
                 st.get("lift", "-"), st.get("liftCI", "-"), verdict))
widths = [max(len(r[i]) for r in rows) for i in range(len(rows[0]))]
for r in rows:
    print("  ".join(c.ljust(w) for c, w in zip(r, widths)).rstrip())
'
