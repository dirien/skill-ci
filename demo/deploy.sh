#!/usr/bin/env bash
# Install or upgrade the controller with its Helm chart (charts/skill-ci-controller).
#   CONTROLLER_TAG=0.5.0 RUNNER_TAG=0.5.1 demo/deploy.sh
# The first run adopts resources applied earlier with `kubectl apply -k config/`.
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

controller_tag="${CONTROLLER_TAG:-0.5.0}"
runner_tag="${RUNNER_TAG:-0.5.1}"
chart="${ROOT_DIR}/charts/skill-ci-controller"
need kubectl
need helm

# Helm installs crds/ only on first install and never upgrades them, so apply them first.
say "Applying the SkillEval CRD"
kubectl apply --server-side -f "${chart}/crds/"

say "Installing the controller ${controller_tag} with runner ${runner_tag} (Helm release skill-ci)"
helm upgrade --install skill-ci "${chart}" \
  --namespace "${NS}" \
  --take-ownership \
  `# the old deploy script set the image and args with kubectl; Helm owns them now` \
  --force-conflicts \
  --set "image.tag=${controller_tag}" \
  --set "controller.runnerImage.tag=${runner_tag}" \
  --wait --timeout 3m

say "Controller is running"
kubectl -n "${NS}" get deployment skill-ci-controller
