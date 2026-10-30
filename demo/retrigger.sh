#!/usr/bin/env bash
# Fire the push webhook again for a tag that already exists, without changing the digest:
# drop the tag through the Harbor API, then create it again with oras tag.
#   demo/retrigger.sh <skill> <version>
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

[[ $# -eq 2 ]] || die "usage: retrigger.sh <skill> <version>"
skill="$1"
version="$2"
[[ "${version}" =~ ${SEMVER_RE} ]] || die "version must look like v1.2.3, got '${version}'"

need kubectl
need python3
wait_toolbox

digest="$(tag_digest "${skill}" "${version}")"
[[ -n "${digest}" ]] || die "${skill}:${version} does not exist, push it first"

say "Re-firing the push webhook for ${skill}:${version}"
note "Removing the tag (deleting a tag fires no webhook) ..."
harbor_api DELETE "/projects/${HARBOR_PROJECT}/repositories/${skill}/artifacts/${digest}/tags/${version}" >/dev/null
note "Creating it again on ${digest:0:19}... (a new tag fires PUSH_ARTIFACT)"
tb oras tag "${ORAS_FLAGS[@]}" "${HARBOR_HOST}/${HARBOR_PROJECT}/${skill}@${digest}" "${version}" </dev/null
