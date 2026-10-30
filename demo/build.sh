#!/usr/bin/env bash
# Build the controller and runner images in the cluster's BuildKit and push them to DOCR.
#   demo/build.sh [controller|runner|all]
# Env: TAG (default 0.1.0), BUILDCTL (default buildctl), DOCKER_CONFIG (DOCR push credentials),
#      REGISTRY, BUILDKIT_PORT (local port for the port-forward, default 1234).
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

what="${1:-all}"
tag="${TAG:-0.1.0}"
registry="${REGISTRY:-registry.digitalocean.com/skill-ci-dirien}"
buildctl="${BUILDCTL:-buildctl}"
port="${BUILDKIT_PORT:-1234}"

case "${what}" in
controller | runner | all) ;;
*) die "usage: build.sh [controller|runner|all]" ;;
esac
need kubectl
need "${buildctl}"
[[ -n "${DOCKER_CONFIG:-}" ]] || note "DOCKER_CONFIG is not set. BuildKit will push with ~/.docker/config.json."

say "Opening a tunnel to the in-cluster BuildKit on localhost:${port}"
kubectl -n "${NS}" port-forward svc/buildkitd "${port}:1234" >/dev/null &
pf_pid=$!
trap 'kill "${pf_pid}" 2>/dev/null || true' EXIT
for _ in $(seq 1 30); do
  "${buildctl}" --addr "tcp://127.0.0.1:${port}" debug workers >/dev/null 2>&1 && break
  sleep 1
done
"${buildctl}" --addr "tcp://127.0.0.1:${port}" debug workers >/dev/null 2>&1 ||
  die "BuildKit is not reachable on localhost:${port}"

# build NAME CONTEXT_DIR DOCKERFILE_DIR
build() {
  local name="$1" context="$2" dockerfile="$3"
  say "Building ${registry}/${name}:${tag}"
  "${buildctl}" --addr "tcp://127.0.0.1:${port}" build \
    --frontend dockerfile.v0 \
    --local "context=${context}" --local "dockerfile=${dockerfile}" \
    --opt platform=linux/amd64 \
    --output "type=image,name=${registry}/${name}:${tag},push=true"
}

if [[ "${what}" == "controller" || "${what}" == "all" ]]; then
  build skill-ci-controller "${ROOT_DIR}/controller" "${ROOT_DIR}/controller"
fi
if [[ "${what}" == "runner" || "${what}" == "all" ]]; then
  build skill-ci-runner "${ROOT_DIR}" "${ROOT_DIR}/runner"
fi
say "Pushed. Deploy with: make deploy TAG=${tag}"
