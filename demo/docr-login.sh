#!/usr/bin/env bash
# Write DOCR push credentials to a Docker config directory, taken from the docr pull secret in the cluster.
# The directory is outside the repository. make build uses it as DOCKER_CONFIG when it exists.
#   DOCR_DOCKER_CONFIG=~/.cache/skill-ci/docker demo/docr-login.sh
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

dir="${DOCR_DOCKER_CONFIG:-${HOME}/.cache/skill-ci/docker}"
need kubectl
need base64

say "Writing DOCR credentials to ${dir}/config.json"
mkdir -p "${dir}"
umask 077
kubectl -n "${NS}" get secret docr -o jsonpath='{.data.\.dockerconfigjson}' | base64 -d >"${dir}/config.json"
note "Done. The file holds a registry token, keep it out of the repository."
