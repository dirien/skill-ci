#!/usr/bin/env bash
# Install the controller: CRD, RBAC, Deployment, Service (kubectl apply -k config/), then pin the image tags.
#   TAG=0.1.0 demo/deploy.sh
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

tag="${TAG:-0.1.0}"
registry="${REGISTRY:-registry.digitalocean.com/skill-ci-dirien}"
need kubectl
need python3

say "Applying config/ (CRD, RBAC, controller)"
kubectl apply -k "${ROOT_DIR}/config/"

say "Setting the controller image to ${registry}/skill-ci-controller:${tag}"
kubectl -n "${NS}" set image deployment/skill-ci-controller "*=${registry}/skill-ci-controller:${tag}"

say "Pointing the controller at the runner image ${registry}/skill-ci-runner:${tag}"
args="$(kubectl -n "${NS}" get deployment skill-ci-controller -o jsonpath='{.spec.template.spec.containers[0].args}')"
patch="$(printf '%s' "${args:-[]}" | python3 -I -c '
import json, sys
args = [a for a in json.load(sys.stdin) if not a.startswith("--runner-image")]
args.append("--runner-image=" + sys.argv[1])
print(json.dumps([{"op": "add", "path": "/spec/template/spec/containers/0/args", "value": args}]))
' "${registry}/skill-ci-runner:${tag}")"
kubectl -n "${NS}" patch deployment skill-ci-controller --type json -p "${patch}"

say "Waiting for the controller"
kubectl -n "${NS}" rollout status deployment/skill-ci-controller --timeout=180s
