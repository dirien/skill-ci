#!/usr/bin/env bash
# Draft evals/evals.json for a skill with SkillEvaluator's `create-eval-dataset --full`, run in a
# short-lived pod from the skill-ci runner image with the Anthropic key of the llm-keys Secret.
# The draft is a starting point for a human to review and edit. It is never part of the gate.
#   demo/draft-evals.sh <skill> [dir]
# The default dir is skills/<skill>; demo/variants/<skill>/<version> works too. An existing
# <dir>/evals/evals.json is left alone unless FORCE=1.
# Runner image, first match wins: RUNNER_IMAGE, the --runner-image of the skill-ci-controller
# Deployment, ghcr.io/dirien/skill-ci-runner:<newest vX.Y.Z tag of this checkout>.
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

# latest_release prints the newest stable release tag without the leading v, or nothing.
latest_release() {
  git -C "${ROOT_DIR}" tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname 2>/dev/null |
    grep -Ev -- '-' | head -n 1 | sed 's/^v//' || true
}

# runner_image prints the image the running controller hands its Jobs, else the newest release's.
runner_image() {
  local args arg tag
  if [[ -n "${RUNNER_IMAGE:-}" ]]; then
    printf '%s' "${RUNNER_IMAGE}"
    return
  fi
  args="$(kubectl -n "${NS}" get deployment skill-ci-controller \
    -o jsonpath='{range .spec.template.spec.containers[0].args[*]}{@}{"\n"}{end}' 2>/dev/null)" || args=""
  while IFS= read -r arg; do
    if [[ "${arg}" == --runner-image=* ]]; then
      printf '%s' "${arg#--runner-image=}"
      return
    fi
  done <<<"${args}"
  tag="$(latest_release)"
  [[ -n "${tag}" ]] || return 0
  printf 'ghcr.io/dirien/skill-ci-runner:%s' "${tag}"
}

[[ $# -ge 1 && -n "${1}" ]] || die "usage: draft-evals.sh <skill> [dir]   (make draft-evals SKILL=<name> [DIR=<skill dir>] [FORCE=1])"
skill="$1"
# agentskills.io names: lowercase letters, digits and single hyphens, at most 64 characters.
[[ "${skill}" =~ ^[a-z0-9]+(-[a-z0-9]+)*$ && ${#skill} -le 64 ]] ||
  die "skill name must be lowercase letters, digits and hyphens (like acme-release-notes), got '${skill}'"
src="${2:-${ROOT_DIR}/skills/${skill}}"
[[ -d "${src}" ]] || die "no skill directory ${src}"
src="$(cd "${src}" && pwd)"
[[ -f "${src}/SKILL.md" ]] || die "no SKILL.md in ${src}"
out="${src}/evals/evals.json"

need kubectl
need tar
need python3

# SkillEvaluator names the dataset after the front matter, so check it before spending an LLM call.
python3 -I - "${src}/SKILL.md" "${skill}" <<'PY' || die "fix the front matter of ${src}/SKILL.md first"
import re, sys
text = open(sys.argv[1], encoding="utf-8").read()
m = re.match(r"---\n(.*?)\n---\n", text, re.S)
if not m:
    sys.exit("error: SKILL.md has no front matter")
if not re.search(r"^name:\s*%s\s*$" % re.escape(sys.argv[2]), m.group(1), re.M):
    sys.exit("error: the front matter name is not %s" % sys.argv[2])
PY

if [[ -e "${out}" ]]; then
  [[ "${FORCE:-0}" == "1" ]] ||
    die "${out#"${ROOT_DIR}"/} already exists. Not overwriting a reviewed dataset. Set FORCE=1 to replace it with a new draft."
  note "FORCE=1: ${out#"${ROOT_DIR}"/} will be replaced by the new draft."
fi

image="$(runner_image)"
[[ -n "${image}" ]] ||
  die "cannot tell the runner image: no skill-ci-controller Deployment and no vX.Y.Z tag. Pass RUNNER_IMAGE=ghcr.io/dirien/skill-ci-runner:0.8.0"
kubectl -n "${NS}" get secret llm-keys >/dev/null 2>&1 ||
  die "Secret llm-keys is missing in namespace ${NS}. It needs the key 'anthropic'."

suffix="$(od -An -N3 -tx1 /dev/urandom | tr -d ' \n')"
pod="draft-evals-${skill:0:40}-${suffix}"
pod="${pod//--/-}"
work="$(mktemp -d)"
cleanup() {
  kubectl -n "${NS}" delete pod "${pod}" --ignore-not-found --wait=true --timeout=60s >/dev/null 2>&1 ||
    printf 'warning: could not delete pod %s/%s, delete it by hand\n' "${NS}" "${pod}" >&2
  rm -rf "${work}"
}
trap cleanup EXIT

say "Drafting evals for ${skill} from ${src#"${ROOT_DIR}"/}"
note "Pod ${NS}/${pod}, image ${image}"
# Only the Anthropic key: SkillEvaluator refuses to guess when several provider keys are set.
# The command is a sleep; the dataset is generated with exec so the log can be checked.
kubectl -n "${NS}" create -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: ${pod}
  labels:
    app.kubernetes.io/name: draft-evals
    app.kubernetes.io/part-of: skill-ci
    skillci.dev/draft-evals-skill: ${skill}
spec:
  restartPolicy: Never
  automountServiceAccountToken: false
  terminationGracePeriodSeconds: 1
  activeDeadlineSeconds: 900
  securityContext:
    runAsNonRoot: true
    runAsUser: 1000
    runAsGroup: 1000
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: draft
      image: ${image}
      command: ["sleep", "900"]
      env:
        - name: SKILL_EVAL_LLM_PROVIDER
          value: anthropic
        - name: ANTHROPIC_API_KEY
          valueFrom:
            secretKeyRef:
              name: llm-keys
              key: anthropic
      resources:
        requests:
          cpu: 100m
          memory: 256Mi
        limits:
          memory: 1Gi
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: ["ALL"]
YAML

note "Waiting for the pod (the first pull of the runner image takes a minute) ..."
if ! kubectl -n "${NS}" wait --for=condition=Ready "pod/${pod}" --timeout=300s >/dev/null 2>&1; then
  reason="$(kubectl -n "${NS}" get pod "${pod}" \
    -o jsonpath='{.status.containerStatuses[0].state.waiting.reason} {.status.containerStatuses[0].state.waiting.message}' 2>/dev/null || true)"
  die "pod ${pod} did not start: ${reason:-unknown reason}"
fi

note "Copying the skill into the pod as skills/${skill}/ (without evals/evals.json) ..."
# COPYFILE_DISABLE keeps macOS tar from adding ._ files.
# shellcheck disable=SC2016  # $1 expands in the pod shell, not here
COPYFILE_DISABLE=1 tar -C "${src}" -cf - . |
  kubectl -n "${NS}" exec -i "${pod}" -- sh -c \
    'mkdir -p "/work/skills/$1" && tar -xf - -C "/work/skills/$1" && rm -f "/work/skills/$1/evals/evals.json"' sh "${skill}"

note "skillevaluator create-eval-dataset skills/${skill} --full (one LLM request) ..."
started="${SECONDS}"
# shellcheck disable=SC2016  # $1 expands in the pod shell, not here
if ! kubectl -n "${NS}" exec "${pod}" -- sh -c 'cd /work && skillevaluator create-eval-dataset "skills/$1" --full' sh "${skill}" \
  >"${work}/log" 2>&1 </dev/null; then
  sed 's/^/    | /' "${work}/log"
  die "skillevaluator create-eval-dataset failed"
fi
sed 's/^/    | /' "${work}/log"
note "Took $((SECONDS - started))s."

# Without a usable provider SkillEvaluator writes generic templates and still exits 0
# (tier3/generate_dataset.py, _generate_with_llm). Only an LLM draft is worth reviewing.
if grep -q 'deterministic template mode' "${work}/log" || ! grep -q 'Using public provider:' "${work}/log"; then
  die "SkillEvaluator fell back to template mode (no usable Anthropic provider). Nothing written. Check the 'anthropic' key in Secret llm-keys."
fi

kubectl -n "${NS}" exec "${pod}" -- cat "/work/skills/${skill}/evals/evals.json" >"${work}/evals.json" </dev/null ||
  die "SkillEvaluator wrote no skills/${skill}/evals/evals.json"

# Validate, normalise and write. "None" (a string) for expected_script or expected_skill is null.
mkdir -p "${src}/evals"
python3 -I - "${work}/evals.json" "${skill}" "${work}/out.json" <<'PY' || die "the draft is not a usable dataset. Nothing written."
import json, sys

path, skill, dest = sys.argv[1], sys.argv[2], sys.argv[3]
try:
    data = json.load(open(path, encoding="utf-8"))
except ValueError as exc:
    sys.exit("error: the draft is not valid JSON: %s" % exc)
if not isinstance(data, dict) or not isinstance(data.get("evals"), list) or not data["evals"]:
    sys.exit("error: the draft has no evals list")
if data.get("skill_name") != skill:
    sys.exit("error: skill_name is %r, not %s" % (data.get("skill_name"), skill))
fixed = 0
for e in data["evals"]:
    if not isinstance(e, dict):
        sys.exit("error: an eval is not an object")
    for key in ("expected_script", "expected_skill"):
        v = e.get(key)
        if isinstance(v, str) and v.strip().lower() in ("", "none", "null"):
            e[key] = None
            fixed += 1
with open(dest, "w", encoding="utf-8") as f:
    json.dump(data, f, indent=2, ensure_ascii=False)
    f.write("\n")
neg = [e for e in data["evals"] if e.get("expected_skill") is None]
print("    %d cases, %d negative, %d \"None\" string(s) normalised to null" % (len(data["evals"]), len(neg), fixed))
for e in data["evals"]:
    tag = " (negative)" if e.get("expected_skill") is None else ""
    print("      - %s%s" % (e.get("id"), tag))
if not neg:
    print("    warning: no negative case. Add one whose prompt is off-skill and expected_skill is null.")
PY
mv "${work}/out.json" "${out}"

say "Draft written to ${out#"${ROOT_DIR}"/}. Review it before you push."
note "[ ] Rewrite prompts and expected_output in a user's words; drop wording copied from SKILL.md."
note "[ ] Add at least one assertion that checks a file on disk (path and content)."
note "[ ] Drop process assertions such as 'reads the SKILL.md first'; keep observable outcomes."
note "[ ] Keep the negative case (expected_skill: null) and make sure its prompt is clearly off-skill."
note "[ ] Check that every id is short, unique and Harbor-safe."
echo
note "Next: make check-skills, then push the version (make demo-push SKILL=${skill} VERSION=vX.Y.Z)."
