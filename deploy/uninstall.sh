#!/usr/bin/env bash
# Remove everything deploy/install.sh created: the controller, Harbor (with its volumes and every skill in
# it), monitoring, the toolbox, the secrets and Agent Sandbox. The cluster itself stays.
#   deploy/uninstall.sh [--yes] [--purge-crds]
#   --yes         do not ask for confirmation
#   --purge-crds  also delete the Prometheus Operator CRDs (cluster-wide; helm uninstall leaves them)
# Safe to run twice: whatever is already gone is skipped.
set -euo pipefail

deploy_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/versions.env
. "${deploy_dir}/versions.env"

assume_yes=0
purge_crds=0
for arg in "$@"; do
  case "${arg}" in
    --yes) assume_yes=1 ;;
    --purge-crds) purge_crds=1 ;;
    *)
      echo "usage: $0 [--yes] [--purge-crds]" >&2
      exit 2
      ;;
  esac
done

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

# helm_uninstall RELEASE NAMESPACE
helm_uninstall() {
  if hm status "$1" --namespace "$2" >/dev/null 2>&1; then
    hm uninstall "$1" --namespace "$2" --wait --timeout 5m
  else
    echo "release $2/$1 not installed"
  fi
}

context="$(kc config current-context)"
echo "This deletes skill-ci, Harbor and monitoring, including all data, from cluster: ${context}"
if [[ "${assume_yes}" != 1 ]]; then
  read -r -p "Type the context name to continue: " answer
  [[ "${answer}" == "${context}" ]] || {
    echo "aborted" >&2
    exit 1
  }
fi

step "SkillEvals, runner Jobs and trial sandboxes"
if kc get crd skillevals.skillci.dev >/dev/null 2>&1; then
  kc -n skill-ci delete skillevals --all --wait=true --timeout=120s || true
fi
kc -n skill-ci delete jobs -l skillci.dev/skill --ignore-not-found --wait=false
if kc get crd sandboxes.agents.x-k8s.io >/dev/null 2>&1; then
  kc -n skill-ci delete sandboxes.agents.x-k8s.io --all --ignore-not-found --wait=false
fi

step "skill-ci controller"
helm_uninstall skill-ci skill-ci
kc delete crd skillevals.skillci.dev --ignore-not-found

step "Harbor setup Job and toolbox"
kc delete -k "${deploy_dir}/manifests/harbor-setup" --ignore-not-found
kc -n skill-ci delete deployment toolbox --ignore-not-found

step "Monitoring"
helm_uninstall kps monitoring
if [[ "${purge_crds}" == 1 ]]; then
  while IFS= read -r crd; do
    kc delete --ignore-not-found "${crd}"
  done < <(kc get crd -o name | grep '\.monitoring\.coreos\.com$' || true)
fi
kc delete namespace monitoring --ignore-not-found

step "Harbor"
helm_uninstall harbor harbor
kc delete namespace harbor --ignore-not-found

step "skill-ci namespace (secrets, model list)"
kc delete namespace skill-ci --ignore-not-found

step "Agent Sandbox ${AGENT_SANDBOX_VERSION}"
kc delete --ignore-not-found -f "https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${AGENT_SANDBOX_VERSION}/sandbox-with-extensions.yaml"

step "Done"
