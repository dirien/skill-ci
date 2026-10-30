#!/usr/bin/env bash
# Shared helpers for the demo scripts. Source it, do not run it.
# shellcheck shell=bash
# shellcheck disable=SC2034  # variables are used by the scripts that source this file

NS="${NS:-skill-ci}"
HARBOR_HOST="${HARBOR_HOST:-harbor.harbor.svc.cluster.local}"
HARBOR_PROJECT="${HARBOR_PROJECT:-skills}"
ARTIFACT_TYPE="application/vnd.agentskill.v1"
SEMVER_RE='^v[0-9]+\.[0-9]+\.[0-9]+$'

DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${DEMO_DIR}/.." && pwd)"

# oras flags for the in-cluster, plain-HTTP Harbor. The registry config is mounted from the harbor-pull secret.
ORAS_FLAGS=(--plain-http --registry-config /auth/config.json)

say() { printf '\n==> %s\n' "$*"; }
note() { printf '    %s\n' "$*"; }
die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || die "'$1' is not installed or not on PATH"
}

# tb runs a command in the oras container of the toolbox pod. It forwards stdin, so loops that
# read from a here-string must not call it without redirecting stdin.
tb() {
  kubectl -n "${NS}" exec -i deploy/toolbox -c oras -- "$@"
}

# harbor_api METHOD PATH [JSON_BODY] calls the Harbor REST API from the curl sidecar of the toolbox pod.
harbor_api() {
  local method="$1" path="$2" body="${3-}"
  # The script runs in the container: $1..$3 are its arguments, the single quotes are intended.
  # shellcheck disable=SC2016
  kubectl -n "${NS}" exec deploy/toolbox -c harbor-api -- \
    sh -c 'curl -sS -f -X "$1" -H "Content-Type: application/json" ${3:+-d "$3"} -u "admin:${HARBOR_ADMIN_PASSWORD}" "http://'"${HARBOR_HOST}"'/api/v2.0$2"' \
    sh "${method}" "${path}" "${body}"
}

# wait_toolbox makes sure the toolbox pod is ready before a script uses it.
wait_toolbox() {
  need kubectl
  kubectl -n "${NS}" rollout status deploy/toolbox --timeout=120s >/dev/null ||
    die "toolbox is not ready. Run 'make up' first."
}

# tag_digest REPO TAG prints the manifest digest, or nothing when the tag does not exist.
tag_digest() {
  local repo="$1" tag="$2" desc
  desc="$(tb oras manifest fetch --descriptor "${ORAS_FLAGS[@]}" "${HARBOR_HOST}/${HARBOR_PROJECT}/${repo}:${tag}" 2>/dev/null </dev/null)" || return 0
  printf '%s' "${desc}" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["digest"])'
}
