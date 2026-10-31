#!/usr/bin/env bash
# Install or upgrade the controller with its Helm chart (charts/skill-ci-controller), pulling the
# images of one release from GHCR: ghcr.io/dirien/skill-ci-{controller,runner,sandbox}:<tag>.
#   demo/deploy.sh                 the newest vX.Y.Z tag of this checkout
#   TAG=0.8.0 demo/deploy.sh       a given release
#   CONTROLLER_TAG=0.8.0 RUNNER_TAG=0.8.0 SANDBOX_TAG=0.8.0 demo/deploy.sh   each image on its own
# The first run adopts resources applied earlier with `kubectl apply -k config/`.
# The GHCR packages are public. If yours are private, add GHCR_PULL_SECRET=<name> (a docker-registry
# Secret in the skill-ci namespace) and it is used for all three images.
# Optional OpenTelemetry (OTLP) push of the controller's skillci_* metrics, off unless OTLP_ENDPOINT is set.
# Same names and meaning as in deploy/install.sh; each maps to one chart value and is passed only when set.
# Set them on every run: a run without OTLP_ENDPOINT turns the export off again.
#   OTLP_ENDPOINT        otlp.endpoint                host:port or URL, e.g. otel-collector.monitoring:4318
#   OTLP_PROTOCOL        otlp.protocol                http/protobuf (chart default) or grpc
#   OTLP_URL_PATH        otlp.urlPath                 e.g. /api/v1/otlp/v1/metrics for the Prometheus OTLP receiver
#   OTLP_INSECURE        otlp.insecure                true for a receiver without TLS inside the cluster
#   OTLP_INTERVAL        otlp.interval                time between exports, e.g. 30s (default 60s)
#   OTLP_HEADERS_SECRET  otlp.headersSecret.name      Secret in skill-ci whose key `headers` holds the ingest key:
#                        kubectl -n skill-ci create secret generic otlp-headers --from-literal=headers='k=v'
# The Pulumi stack's ServiceMonitor already scrapes the controller into its Prometheus: do not push into that
# Prometheus too, or every series is stored twice.
#   OTLP_ENDPOINT=otel-collector.monitoring:4318 OTLP_INSECURE=true demo/deploy.sh
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

# otlp_set_args fills otlp_args with the chart values of the OTLP_* variables, nothing when OTLP_ENDPOINT
# is empty. The same function is in deploy/install.sh: keep both identical.
otlp_args=()
otlp_set_args() {
  if [[ -z "${OTLP_ENDPOINT:-}" ]]; then
    [[ -z "${OTLP_PROTOCOL:-}${OTLP_URL_PATH:-}${OTLP_INSECURE:-}${OTLP_INTERVAL:-}${OTLP_HEADERS_SECRET:-}" ]] ||
      die "an OTLP_* variable is set but OTLP_ENDPOINT is empty"
    return 0
  fi
  otlp_args=(--set-string "otlp.endpoint=${OTLP_ENDPOINT}")
  case "${OTLP_PROTOCOL:-}" in
    "") ;;
    http/protobuf | grpc) otlp_args+=(--set-string "otlp.protocol=${OTLP_PROTOCOL}") ;;
    *) die "OTLP_PROTOCOL must be http/protobuf or grpc, not ${OTLP_PROTOCOL}" ;;
  esac
  [[ -z "${OTLP_URL_PATH:-}" ]] || otlp_args+=(--set-string "otlp.urlPath=${OTLP_URL_PATH}")
  case "${OTLP_INSECURE:-}" in
    "") ;;
    true | false) otlp_args+=(--set "otlp.insecure=${OTLP_INSECURE}") ;;
    *) die "OTLP_INSECURE must be true or false, not ${OTLP_INSECURE}" ;;
  esac
  [[ -z "${OTLP_INTERVAL:-}" ]] || otlp_args+=(--set-string "otlp.interval=${OTLP_INTERVAL}")
  [[ -z "${OTLP_HEADERS_SECRET:-}" ]] || otlp_args+=(--set-string "otlp.headersSecret.name=${OTLP_HEADERS_SECRET}")
  return 0
}

# latest_release prints the newest stable release tag without the leading v, or nothing.
latest_release() {
  git -C "${ROOT_DIR}" tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname 2>/dev/null |
    grep -Ev -- '-' | head -n 1 | sed 's/^v//' || true
}

tag="${TAG:-$(latest_release)}"
[[ -n "${tag}" || -n "${CONTROLLER_TAG:-}" ]] ||
  die "no release tag found. Push a vX.Y.Z tag (README, Releases) or pass TAG=0.8.0"
controller_tag="${CONTROLLER_TAG:-${tag}}"
runner_tag="${RUNNER_TAG:-${tag}}"
sandbox_tag="${SANDBOX_TAG:-${runner_tag}}"
chart="${ROOT_DIR}/charts/skill-ci-controller"
need kubectl
need helm
otlp_set_args
if [[ -n "${OTLP_HEADERS_SECRET:-}" ]] && ! kubectl -n "${NS}" get secret "${OTLP_HEADERS_SECRET}" >/dev/null 2>&1; then
  die "OTLP_HEADERS_SECRET: Secret ${NS}/${OTLP_HEADERS_SECRET} does not exist. Create it first:
  kubectl -n ${NS} create secret generic ${OTLP_HEADERS_SECRET} --from-literal=headers='<key>=<value>'"
fi
if [[ "${OTLP_ENDPOINT:-}" == *kps-kube-prometheus-stack-prometheus* ]]; then
  echo "warning: OTLP_ENDPOINT is the Prometheus that scrapes the controller already; every series would be stored twice" >&2
fi

# Helm installs crds/ only on first install and never upgrades them, so apply them first.
say "Applying the SkillEval CRD"
kubectl apply --server-side -f "${chart}/crds/"

pull_secret=()
if [[ -n "${GHCR_PULL_SECRET:-}" ]]; then
  pull_secret=(--set "imagePullSecrets[0].name=${GHCR_PULL_SECRET}")
fi

say "Installing controller ${controller_tag}, runner ${runner_tag}, sandbox ${sandbox_tag} (Helm release skill-ci)${OTLP_ENDPOINT:+, OTLP export to ${OTLP_ENDPOINT}}"
helm upgrade --install skill-ci "${chart}" \
  --namespace "${NS}" \
  --take-ownership \
  `# the old deploy script set the image and args with kubectl; Helm owns them now` \
  --force-conflicts \
  --set "image.tag=${controller_tag}" \
  --set "controller.runnerImage.tag=${runner_tag}" \
  --set "controller.sandboxImage.tag=${sandbox_tag}" \
  ${pull_secret[@]+"${pull_secret[@]}"} \
  ${otlp_args[@]+"${otlp_args[@]}"} \
  --wait --timeout 3m

say "Controller is running"
kubectl -n "${NS}" get deployment skill-ci-controller
