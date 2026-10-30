#!/usr/bin/env bash
# The "push a new version" beat of the demo.
#   demo/demo-push.sh [skill] [version]
# Defaults: acme-release-notes v1.1.0, an improved version. v1.2.0 is a deliberate regression.
# Variants live in demo/variants/<skill>/<version>/.
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

skill="${1:-acme-release-notes}"
version="${2:-v1.1.0}"

say "Publishing a new skill version: ${skill}:${version}"
"${DEMO_DIR}/push-skill.sh" "${skill}" "${version}"

echo
note "Watch the pipeline react:  kubectl -n ${NS} get skillevals -w"
note "Or in a loop:               make status"
