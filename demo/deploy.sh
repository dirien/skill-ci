#!/usr/bin/env bash
# Install or upgrade the controller with its Helm chart (charts/skill-ci-controller), pulling the
# images of one release from GHCR: ghcr.io/dirien/skill-ci-{controller,runner,sandbox}:<tag>.
#   demo/deploy.sh                 the newest vX.Y.Z tag of this checkout
#   TAG=0.7.0 demo/deploy.sh       a given release
#   CONTROLLER_TAG=0.7.0 RUNNER_TAG=0.7.0 SANDBOX_TAG=0.7.0 demo/deploy.sh   each image on its own
# The first run adopts resources applied earlier with `kubectl apply -k config/`.
# The GHCR packages are public. If yours are private, add GHCR_PULL_SECRET=<name> (a docker-registry
# Secret in the skill-ci namespace) and it is used for all three images.
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

# latest_release prints the newest stable release tag without the leading v, or nothing.
latest_release() {
  git -C "${ROOT_DIR}" tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname 2>/dev/null |
    grep -Ev -- '-' | head -n 1 | sed 's/^v//' || true
}

tag="${TAG:-$(latest_release)}"
[[ -n "${tag}" || -n "${CONTROLLER_TAG:-}" ]] ||
  die "no release tag found. Push a vX.Y.Z tag (README, Releases) or pass TAG=0.7.0"
controller_tag="${CONTROLLER_TAG:-${tag}}"
runner_tag="${RUNNER_TAG:-${tag}}"
sandbox_tag="${SANDBOX_TAG:-${runner_tag}}"
chart="${ROOT_DIR}/charts/skill-ci-controller"
need kubectl
need helm

# Helm installs crds/ only on first install and never upgrades them, so apply them first.
say "Applying the SkillEval CRD"
kubectl apply --server-side -f "${chart}/crds/"

pull_secret=()
if [[ -n "${GHCR_PULL_SECRET:-}" ]]; then
  pull_secret=(--set "imagePullSecrets[0].name=${GHCR_PULL_SECRET}")
fi

say "Installing controller ${controller_tag}, runner ${runner_tag}, sandbox ${sandbox_tag} (Helm release skill-ci)"
helm upgrade --install skill-ci "${chart}" \
  --namespace "${NS}" \
  --take-ownership \
  `# the old deploy script set the image and args with kubectl; Helm owns them now` \
  --force-conflicts \
  --set "image.tag=${controller_tag}" \
  --set "controller.runnerImage.tag=${runner_tag}" \
  --set "controller.sandboxImage.tag=${sandbox_tag}" \
  ${pull_secret[@]+"${pull_secret[@]}"} \
  --wait --timeout 3m

say "Controller is running"
kubectl -n "${NS}" get deployment skill-ci-controller
