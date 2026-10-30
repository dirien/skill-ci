#!/usr/bin/env bash
# Check that infra/Pulumi.yaml uses the same component versions as deploy/versions.env.
#   deploy/check-versions.sh
# Exits 1 and prints every mismatch. The skill-ci release is not in Pulumi.yaml (make deploy installs it).
set -euo pipefail

deploy_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
pulumi_yaml="${PULUMI_YAML:-${deploy_dir}/../infra/Pulumi.yaml}"
# shellcheck source=deploy/versions.env
. "${deploy_dir}/versions.env"

# pulumi_default KEY prints the default of a config key in Pulumi.yaml.
pulumi_default() {
  awk -v key="$1" '
    /^config:/ { in_config = 1; next }
    /^[a-z]/ { in_config = 0 }
    in_config && $0 ~ "^  " key ":" { found = 1; next }
    in_config && found && /^    default:/ { gsub(/^    default:[ ]*|"/, ""); print; exit }
    in_config && /^  [a-zA-Z]+:/ { found = 0 }
  ' "${pulumi_yaml}"
}

status=0
# check PULUMI_KEY VERSIONS_ENV_VALUE
check() {
  local want="$2" got
  got="$(pulumi_default "$1")"
  if [[ "${got}" == "${want}" ]]; then
    printf 'ok       %-28s %s\n' "$1" "${got}"
  else
    printf 'MISMATCH %-28s Pulumi.yaml=%s versions.env=%s\n' "$1" "${got:-<missing>}" "${want}" >&2
    status=1
  fi
}

check agentSandboxVersion "${AGENT_SANDBOX_VERSION}"
check harborChartVersion "${HARBOR_CHART_VERSION}"
check kubePrometheusStackVersion "${KUBE_PROMETHEUS_STACK_VERSION}"
check orasVersion "${ORAS_VERSION}"
check curlVersion "${CURL_VERSION}"
exit "${status}"
