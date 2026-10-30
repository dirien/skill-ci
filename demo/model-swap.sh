#!/usr/bin/env bash
# Change the model list live. The controller watches the ConfigMap and evaluates every
# beta/stable skill digest against each model that was added.
#   demo/model-swap.sh add      add claude-code/claude-opus-5-5 as an optional model (default)
#   demo/model-swap.sh swap     replace claude-sonnet-5-5 with claude-opus-5-5 (required)
#   demo/model-swap.sh restore  back to demo/models.yaml
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

mode="${1:-add}"
base="${DEMO_DIR}/models.yaml"
need kubectl
need python3

case "${mode}" in
add)
  models="$(cat "${base}"
    printf '  - agent: claude-code\n    model: claude-opus-5-5\n    required: false\n')"
  ;;
swap)
  models="$(printf 'models:\n  - agent: claude-code\n    model: claude-haiku-5-5\n    required: true\n  - agent: claude-code\n    model: claude-opus-5-5\n    required: true\n')"
  ;;
restore)
  models="$(cat "${base}")"
  ;;
*)
  die "unknown mode '${mode}'. Use add, swap, or restore."
  ;;
esac

say "Model list (${mode})"
printf '%s\n' "${models}" | sed 's/^/    /'

patch="$(printf '%s' "${models}" | python3 -I -c 'import json,sys; print(json.dumps({"data": {"models.yaml": sys.stdin.read() + "\n"}}))')"
kubectl -n "${NS}" patch configmap skill-ci-models --type merge -p "${patch}"
note "ConfigMap skill-ci-models updated. The controller picks up the change on its own."
