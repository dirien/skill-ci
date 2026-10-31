#!/usr/bin/env bash
# Install or upgrade the skill-ci release on the Pulumi stack. The controller is a Helm release of the
# published OCI chart (oci://ghcr.io/dirien/charts/skill-ci-controller) and the SkillEval CRD is applied
# from the chart's crds/: both are resources in infra/Pulumi.yaml, so this is `pulumi up` with a version.
#   demo/deploy.sh               the stack's skillCiVersion (default: SKILL_CI_VERSION of deploy/versions.env)
#   TAG=0.8.1 demo/deploy.sh     a given release: sets the stack config skillCiVersion first
# The controller, runner and sandbox images are always the ones of that release.
# Optional OpenTelemetry (OTLP) push of the controller's skillci_* metrics is stack config too, so it stays
# set across runs (keys and meaning in infra/Pulumi.yaml, values in CONTRACT.md "OpenTelemetry export"):
#   cd infra && pulumi config set otlpEndpoint otel-collector.monitoring:4318 && pulumi config set otlpInsecure true
# The stack's ServiceMonitor already scrapes the controller into its Prometheus: do not push into that
# Prometheus too, or every series is stored twice.
# Clusters without the Pulumi stack use deploy/install.sh instead.
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

need pulumi
need kubectl

pulumi_env="${PULUMI_ENV:-${HOME}/.pulumi-local/skill-ci-env.sh}"
if [[ -f "${pulumi_env}" ]]; then
  # shellcheck source=/dev/null
  . "${pulumi_env}"
fi
cd "${ROOT_DIR}/infra"

if [[ -n "${TAG:-}" ]]; then
  tag="${TAG#v}"
  [[ "${tag}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || die "TAG must look like 0.8.1, got '${TAG}'"
  say "Setting the stack's skillCiVersion to ${tag}"
  pulumi config set skillCiVersion "${tag}"
fi

say "Installing skill-ci $(pulumi config get skillCiVersion 2>/dev/null || echo "(default from Pulumi.yaml)") with pulumi up"
pulumi up --yes

say "Controller is running"
kubectl -n "${NS}" rollout status deployment/skill-ci-controller --timeout=180s
kubectl -n "${NS}" get deployment skill-ci-controller -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
