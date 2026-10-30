#!/usr/bin/env bash
# Push one skill version to Harbor from inside the cluster, through the toolbox pod.
#   demo/push-skill.sh <skill> <version> [dir]
# Source directory, first match wins: the dir argument, demo/variants/<skill>/<version>, skills/<skill>.
# An existing tag is left alone unless FORCE=1.
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

[[ $# -ge 2 ]] || die "usage: push-skill.sh <skill> <version> [dir]"
skill="$1"
version="$2"
[[ "${version}" =~ ${SEMVER_RE} ]] || die "version must look like v1.2.3, got '${version}'"

if [[ -n "${3-}" ]]; then
  src="$3"
elif [[ -d "${DEMO_DIR}/variants/${skill}/${version}" ]]; then
  src="${DEMO_DIR}/variants/${skill}/${version}"
else
  src="${ROOT_DIR}/skills/${skill}"
fi
[[ -f "${src}/SKILL.md" ]] || die "no SKILL.md in ${src}"

need kubectl
need tar
need python3
wait_toolbox

existing="$(tag_digest "${skill}" "${version}")"
if [[ -n "${existing}" && "${FORCE:-0}" != "1" ]]; then
  note "${skill}:${version} is already in Harbor (${existing:0:19}...). Skipping. Set FORCE=1 to push again."
  exit 0
fi

say "Pushing ${skill}:${version} from ${src#"${ROOT_DIR}"/}"
note "Copying the skill into the toolbox pod as ${skill}/ ..."
# COPYFILE_DISABLE keeps macOS tar from adding ._ files.
# shellcheck disable=SC2016  # $1 expands in the toolbox shell, not here
COPYFILE_DISABLE=1 tar -C "${src}" -cf - . |
  tb sh -c 'rm -rf "/workspace/$1" && mkdir -p "/workspace/$1" && tar -xf - -C "/workspace/$1"' sh "${skill}"

note "oras push ${HARBOR_HOST}/${HARBOR_PROJECT}/${skill}:${version}"
tb sh -c 'cd /workspace && oras push "$@"' sh \
  "${ORAS_FLAGS[@]}" \
  "${HARBOR_HOST}/${HARBOR_PROJECT}/${skill}:${version}" \
  --artifact-type "${ARTIFACT_TYPE}" "${skill}/" </dev/null

note "Harbor now fires PUSH_ARTIFACT at the controller for ${skill}:${version}."
