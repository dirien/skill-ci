#!/usr/bin/env bash
# The live push on stage: a small, real edit of k8s-incident-triage v1.1.0 (the stable version of the
# demo state), pushed as a new version. Harbor sees a new digest and fires the webhook, the controller
# starts one SkillEval per model list entry. The result arrives about 12 minutes later.
#   demo/live-push.sh v1.1.1      the next free version after v1.1.0; make status lists the taken ones
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

[[ $# -eq 1 ]] || die "usage: live-push.sh <version>, the next free version after v1.1.0 (v1.1.1, v1.1.2, ...)"
version="$1"
[[ "${version}" =~ ${SEMVER_RE} ]] || die "version must look like v1.2.3, got '${version}'"
case "${version}" in
v1.0.0 | v1.1.0 | v1.2.0) die "${version} is part of the demo state; use the next free version, for example v1.1.1" ;;
esac

work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
cp -R "${DEMO_DIR}/variants/k8s-incident-triage/v1.1.0" "${work}/k8s-incident-triage"
skill_md="${work}/k8s-incident-triage/SKILL.md"
# The edit: a new version number and one more rule, so the digest is new.
python3 -I - "${skill_md}" "${version#v}" <<'PY'
import re, sys
path, version = sys.argv[1], sys.argv[2]
text = open(path).read()
text = re.sub(r"(?m)^  version: .*$", "  version: " + version, text, count=1)
text += "\nKeep the whole note under 15 lines. The next person on call reads it on a phone.\n"
open(path, "w").write(text)
PY

"${DEMO_DIR}/push-skill.sh" k8s-incident-triage "${version}" "${work}/k8s-incident-triage"
echo
note "Watch the pipeline react:  make results SKILL=k8s-incident-triage"
