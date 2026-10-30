#!/usr/bin/env bash
# Port-forward Grafana and print where to log in. Stops with Ctrl-C.
#   PORT=3000 demo/dashboard.sh
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

port="${PORT:-3000}"
need kubectl

say "Grafana is at http://localhost:${port}  (dashboard: skill-ci)"
note "User:     admin"
note "Password: kubectl -n monitoring get secret grafana-admin -o jsonpath='{.data.admin-password}' | base64 -d"
note "          (or: cd infra && pulumi stack output grafanaAdminPassword --show-secrets)"
echo
exec kubectl -n monitoring port-forward svc/kps-grafana "${port}:80"
