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
#   CONTROLLER_CHART   chart to install (default oci://ghcr.io/dirien/charts/skill-ci-controller),
#                      for example ./charts/skill-ci-controller to test an unreleased chart
#   KUBECONFIG, KUBE_CONTEXT   as for kubectl (KUBE_CONTEXT selects a context for kubectl and helm)
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
# The password goes through a private values file, not the command line.
printf 'harborAdminPassword: "%s"\n' "${harbor_password}" >"${tmp}/harbor-password.yaml"
hm upgrade --install harbor harbor \
  --repo https://helm.goharbor.io --version "${HARBOR_CHART_VERSION}" \
  --namespace harbor --create-namespace \
  -f "${deploy_dir}/helm/values/harbor.yaml" -f "${tmp}/harbor-password.yaml" \
  --wait --timeout 15m
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

step "skill-ci controller ${SKILL_CI_VERSION} (${controller_chart})"
chart_version=()
[[ "${controller_chart}" != oci://* ]] || chart_version=(--version "${SKILL_CI_VERSION}")
# Helm installs crds/ on the first install only and never upgrades them, so apply them on every run.
hm show crds "${controller_chart}" ${chart_version[@]+"${chart_version[@]}"} | kc apply --server-side -f -
hm upgrade --install skill-ci "${controller_chart}" ${chart_version[@]+"${chart_version[@]}"} \
  --namespace "${ns}" \
  -f "${deploy_dir}/helm/values/skill-ci-controller.yaml" \
  --set-string "image.tag=${SKILL_CI_VERSION}" \
  --set-string "controller.runnerImage.tag=${SKILL_CI_VERSION}" \
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
