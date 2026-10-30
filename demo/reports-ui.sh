#!/usr/bin/env bash
# Port-forward the controller's reporting site and print its URL. Stops with Ctrl-C.
#   PORT=8082 demo/reports-ui.sh
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

port="${PORT:-8082}"
need kubectl

say "Reporting site is at http://localhost:${port}"
note "Runs, release channels, policy decisions and the stored SkillEvaluator reports."
note "Needs controller image 0.3.0 or newer. Fallback without the cluster: make report-page"
echo
exec kubectl -n "${NS}" port-forward svc/skill-ci-controller "${port}:8082"
