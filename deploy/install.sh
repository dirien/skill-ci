#!/usr/bin/env bash
# Install skill-ci on the cluster your kubeconfig points at, with plain Helm charts and kubectl (no Pulumi).
#
#   export ANTHROPIC_API_KEY=sk-ant-...        # required on the first run
#   export OPENAI_API_KEY=...                  # optional, see deploy/README.md for the others
#   deploy/install.sh
#
# Idempotent: a second run upgrades what changed and keeps the generated passwords. Every step is the
# command from deploy/README.md. Environment:
#   ANTHROPIC_API_KEY  required unless the Secret llm-keys already exists
#   OPENAI_API_KEY, OPENAI_BASE_URL, OPENROUTER_API_KEY, OLLAMA_API_KEY, NVIDIA_API_KEY   optional LLM keys
#   SKIP_MONITORING=1  do not install kube-prometheus-stack, the ServiceMonitor and the dashboard
#   HARBOR_UPGRADE=1   run helm upgrade for Harbor even when its chart version and values did not change
#   CONTROLLER_CHART   chart to install (default oci://ghcr.io/dirien/charts/skill-ci-controller),
#                      for example ./charts/skill-ci-controller to test an unreleased chart
#   KUBECONFIG, KUBE_CONTEXT   as for kubectl (KUBE_CONTEXT selects a context for kubectl and helm)
# Optional OpenTelemetry (OTLP) push of the controller's skillci_* metrics, off unless OTLP_ENDPOINT is set.
# Each variable maps to one chart value and is passed only when set. Set them on every run: a run without
# OTLP_ENDPOINT installs the chart without them, which turns the export off again.
#   OTLP_ENDPOINT        otlp.endpoint                host:port or URL, e.g. otel-collector.monitoring:4318
#   OTLP_PROTOCOL        otlp.protocol                http/protobuf (chart default) or grpc
#   OTLP_URL_PATH        otlp.urlPath                 e.g. /api/v1/otlp/v1/metrics for the Prometheus OTLP receiver
#   OTLP_INSECURE        otlp.insecure                true for a receiver without TLS inside the cluster
#   OTLP_INTERVAL        otlp.interval                time between exports, e.g. 30s (default 60s)
#   OTLP_HEADERS_SECRET  otlp.headersSecret.name      Secret in skill-ci whose key `headers` holds the ingest key:
#                        kubectl -n skill-ci create secret generic otlp-headers --from-literal=headers='k=v'
# Do not push into the Prometheus that already scrapes the controller (the ServiceMonitor of this install):
# every series would be stored twice. See deploy/README.md "OpenTelemetry export".
set -euo pipefail

deploy_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/versions.env
. "${deploy_dir}/versions.env"

controller_chart="${CONTROLLER_CHART:-oci://ghcr.io/dirien/charts/skill-ci-controller}"
ns=skill-ci
harbor_host=harbor.harbor.svc.cluster.local

kctx=()
hctx=()
if [[ -n "${KUBE_CONTEXT:-}" ]]; then
  kctx=(--context "${KUBE_CONTEXT}")
  hctx=(--kube-context "${KUBE_CONTEXT}")
fi
# ${arr[@]+...}: an empty array is fine under set -u on macOS bash 3.2 too.
kc() { kubectl ${kctx[@]+"${kctx[@]}"} "$@"; }
hm() { helm ${hctx[@]+"${hctx[@]}"} "$@"; }

# otlp_set_args fills otlp_args with the chart values of the OTLP_* variables, nothing when OTLP_ENDPOINT
# is empty. The same function is in demo/deploy.sh: keep both identical.
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

step_no=0
step() {
  step_no=$((step_no + 1))
  printf '\n==> [%d] %s\n' "${step_no}" "$*"
}
die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

# secret_field NAMESPACE SECRET KEY prints a decoded field of a Secret, or nothing when it does not exist.
secret_field() {
  kc -n "$1" get secret "$2" -o "jsonpath={.data.$3}" 2>/dev/null | base64 --decode 2>/dev/null || true
}

# apply_secret NAMESPACE NAME [kubectl create secret args...]: create or update a Secret without
# putting its values on a command line (callers pass --from-file/--from-env-file with process substitution).
apply_secret() {
  local namespace="$1" name="$2"
  shift 2
  kc -n "${namespace}" create secret generic "${name}" "$@" --dry-run=client -o yaml | kc apply -f - >/dev/null
  echo "secret ${namespace}/${name} applied"
}

step "Checking prerequisites"
command -v kubectl >/dev/null || die "kubectl is not installed"
command -v helm >/dev/null || die "helm is not installed"
command -v openssl >/dev/null || die "openssl is not installed (used to generate passwords)"
helm_major="$(helm version --template '{{.Version}}' | sed 's/^v//; s/\..*//')"
[[ "${helm_major}" -ge 3 ]] || die "helm 3.17 or newer is required"
kc version >/dev/null || die "cannot reach the cluster. Check KUBECONFIG"
echo "cluster: $(kc config current-context)"
if ! kc get storageclass -o jsonpath='{range .items[*]}{.metadata.annotations.storageclass\.kubernetes\.io/is-default-class}{"\n"}{end}' | grep -q true; then
  die "no default StorageClass. Harbor and Prometheus need ReadWriteOnce volumes (see deploy/README.md)"
fi
if [[ -z "${ANTHROPIC_API_KEY:-}" && -z "$(secret_field "${ns}" llm-keys anthropic)" ]]; then
  die "ANTHROPIC_API_KEY is not set and the Secret llm-keys does not exist yet"
fi
otlp_set_args
if [[ -n "${OTLP_HEADERS_SECRET:-}" ]] && ! kc -n "${ns}" get secret "${OTLP_HEADERS_SECRET}" >/dev/null 2>&1; then
  die "OTLP_HEADERS_SECRET: Secret ${ns}/${OTLP_HEADERS_SECRET} does not exist. Create it first:
  kubectl apply -f deploy/manifests/namespaces.yaml
  kubectl -n ${ns} create secret generic ${OTLP_HEADERS_SECRET} --from-literal=headers='<key>=<value>'"
fi
if [[ "${OTLP_ENDPOINT:-}" == *kps-kube-prometheus-stack-prometheus* && "${SKIP_MONITORING:-0}" != 1 ]]; then
  echo "warning: OTLP_ENDPOINT is the Prometheus that scrapes the controller already; every series would be stored twice" >&2
fi

step "Namespaces"
kc apply -f "${deploy_dir}/manifests/namespaces.yaml"
if [[ "${SKIP_MONITORING:-0}" != 1 ]]; then
  kc apply -f "${deploy_dir}/manifests/monitoring/namespace.yaml"
fi

step "Agent Sandbox ${AGENT_SANDBOX_VERSION}"
kc apply --server-side -f "https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${AGENT_SANDBOX_VERSION}/sandbox-with-extensions.yaml"
kc -n agent-sandbox-system rollout status deployment/agent-sandbox-controller --timeout=180s

step "Secrets"
# The Harbor admin password is only read when Harbor first initializes its database, so a re-run must reuse it.
harbor_password="$(secret_field "${ns}" harbor-credentials password)"
if [[ -z "${harbor_password}" ]]; then
  harbor_password="Hb1$(openssl rand -hex 10)"
  echo "generated a Harbor admin password"
fi
webhook_token="$(secret_field "${ns}" harbor-webhook token)"
if [[ -z "${webhook_token}" ]]; then
  webhook_token="$(openssl rand -hex 20)"
  echo "generated a Harbor webhook token"
fi
apply_secret "${ns}" harbor-credentials \
  --from-env-file=<(printf 'username=admin\npassword=%s\n' "${harbor_password}")
apply_secret "${ns}" harbor-webhook \
  --from-env-file=<(printf 'token=%s\n' "${webhook_token}")
harbor_auth="$(printf 'admin:%s' "${harbor_password}" | base64 | tr -d '\n')"
apply_secret "${ns}" harbor-pull --type=kubernetes.io/dockerconfigjson \
  --from-file=.dockerconfigjson=<(printf '{"auths":{"%s":{"username":"admin","password":"%s","auth":"%s"}}}' \
    "${harbor_host}" "${harbor_password}" "${harbor_auth}")
if [[ -n "${ANTHROPIC_API_KEY:-}" ]]; then
  apply_secret "${ns}" llm-keys --from-env-file=<(
    printf 'anthropic=%s\n' "${ANTHROPIC_API_KEY}"
    [[ -z "${OPENAI_API_KEY:-}" ]] || printf 'openai=%s\n' "${OPENAI_API_KEY}"
    [[ -z "${OPENAI_BASE_URL:-}" ]] || printf 'openai-base-url=%s\n' "${OPENAI_BASE_URL}"
    [[ -z "${OPENROUTER_API_KEY:-}" ]] || printf 'openrouter=%s\n' "${OPENROUTER_API_KEY}"
    [[ -z "${OLLAMA_API_KEY:-}" ]] || printf 'ollama=%s\n' "${OLLAMA_API_KEY}"
    [[ -z "${NVIDIA_API_KEY:-}" ]] || printf 'nvidia=%s\n' "${NVIDIA_API_KEY}"
  )
else
  echo "secret ${ns}/llm-keys kept (ANTHROPIC_API_KEY is not set)"
fi
if [[ "${SKIP_MONITORING:-0}" != 1 ]]; then
  grafana_password="$(secret_field monitoring grafana-admin admin-password)"
  [[ -n "${grafana_password}" ]] || grafana_password="$(openssl rand -hex 12)"
  apply_secret monitoring grafana-admin \
    --from-env-file=<(printf 'admin-user=admin\nadmin-password=%s\n' "${grafana_password}")
fi

step "Harbor ${HARBOR_CHART_VERSION}"
# The chart generates its internal secrets on every render, so each upgrade restarts core, jobservice
# and registry. Upgrade only when the chart version or the values changed (HARBOR_UPGRADE=1 forces it).
harbor_inputs="$(cat "${deploy_dir}/helm/values/harbor.yaml" <(printf '%s\n' "${HARBOR_CHART_VERSION}") |
  openssl dgst -sha256 | sed 's/^.* //')"
harbor_applied="$(kc get namespace harbor -o jsonpath='{.metadata.annotations.skillci\.dev/harbor-inputs}' 2>/dev/null || true)"
if [[ "${harbor_applied}" == "${harbor_inputs}" && "${HARBOR_UPGRADE:-0}" != 1 ]] &&
  hm -n harbor status harbor >/dev/null 2>&1; then
  echo "harbor ${HARBOR_CHART_VERSION} is installed with these values, not upgraded (HARBOR_UPGRADE=1 forces it)"
else
  # The password goes through a private values file, not the command line.
  printf 'harborAdminPassword: "%s"\n' "${harbor_password}" >"${tmp}/harbor-password.yaml"
  hm upgrade --install harbor harbor \
    --repo https://helm.goharbor.io --version "${HARBOR_CHART_VERSION}" \
    --namespace harbor --create-namespace \
    -f "${deploy_dir}/helm/values/harbor.yaml" -f "${tmp}/harbor-password.yaml" \
    --wait --timeout 15m
  kc annotate namespace harbor --overwrite "skillci.dev/harbor-inputs=${harbor_inputs}" >/dev/null
fi
kc -n harbor get pods

if [[ "${SKIP_MONITORING:-0}" != 1 ]]; then
  step "kube-prometheus-stack ${KUBE_PROMETHEUS_STACK_VERSION}"
  hm upgrade --install kps kube-prometheus-stack \
    --repo https://prometheus-community.github.io/helm-charts --version "${KUBE_PROMETHEUS_STACK_VERSION}" \
    --namespace monitoring \
    -f "${deploy_dir}/helm/values/kube-prometheus-stack.yaml" \
    --wait --timeout 10m
  kc apply -k "${deploy_dir}/manifests/monitoring"
fi

step "Toolbox"
kc apply -k "${deploy_dir}/manifests"
kc -n "${ns}" rollout status deployment/toolbox --timeout=180s

step "Model list (created once, never overwritten: the demo edits it live)"
if kc -n "${ns}" get configmap skill-ci-models >/dev/null 2>&1; then
  echo "configmap ${ns}/skill-ci-models exists, left as it is"
else
  kc create -f "${deploy_dir}/manifests/seed/skill-ci-models.yaml"
fi

step "skill-ci controller ${SKILL_CI_VERSION} (${controller_chart})${OTLP_ENDPOINT:+, OTLP export to ${OTLP_ENDPOINT}}"
chart_version=()
[[ "${controller_chart}" != oci://* ]] || chart_version=(--version "${SKILL_CI_VERSION}")
# Helm installs crds/ on the first install only and never upgrades them, so apply them on every run.
hm show crds "${controller_chart}" ${chart_version[@]+"${chart_version[@]}"} | kc apply --server-side -f -
hm upgrade --install skill-ci "${controller_chart}" ${chart_version[@]+"${chart_version[@]}"} \
  --namespace "${ns}" \
  -f "${deploy_dir}/helm/values/skill-ci-controller.yaml" \
  --set-string "image.tag=${SKILL_CI_VERSION}" \
  --set-string "controller.runnerImage.tag=${SKILL_CI_VERSION}" \
  --set-string "controller.sandboxImage.tag=${SKILL_CI_VERSION}" \
  ${otlp_args[@]+"${otlp_args[@]}"} \
  --wait --timeout 5m
kc -n "${ns}" get deployment skill-ci-controller

step "Harbor setup Job (project skills, webhook policy)"
kc -n "${ns}" delete job harbor-setup --ignore-not-found --wait=true
kc apply -k "${deploy_dir}/manifests/harbor-setup"
if ! kc -n "${ns}" wait --for=condition=complete job/harbor-setup --timeout=10m; then
  kc -n "${ns}" logs job/harbor-setup --tail=30 || true
  die "the Harbor setup Job did not complete"
fi
kc -n "${ns}" logs job/harbor-setup --tail=5

step "Done"
cat <<DONE
Harbor admin password:  kubectl -n ${ns} get secret harbor-credentials -o jsonpath='{.data.password}' | base64 --decode
Next: make seed   (pushes the sample skills, see deploy/README.md "Run the demo")
DONE
