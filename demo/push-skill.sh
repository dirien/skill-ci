#!/usr/bin/env bash
# Push one skill version to Harbor from inside the cluster, through the toolbox pod.
#   demo/push-skill.sh <skill> <version> [dir]
# Source directory, first match wins: the dir argument, demo/variants/<skill>/<version>, skills/<skill>.
# An existing tag is left alone unless FORCE=1.
#
# The network metadata of SKILL.md (skillci.dev/network: none | allowlist | full and
# skillci.dev/network-allow: "@python,dl.k8s.io") becomes manifest annotations with the same keys,
# which the controller reads. Without this script, push the same way with plain oras:
#   oras push --annotation skillci.dev/network=allowlist \
#     --annotation skillci.dev/network-allow=@python,dl.k8s.io \
#     <registry>/skills/<skill>:<version> --artifact-type application/vnd.agentskill.v1 <skill>/
#   demo/push-skill.sh --network-annotations <dir>   validates and prints them only
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

# network_annotations DIR validates the network metadata in DIR/SKILL.md and prints one
# key=value line per key it declares, nothing when it declares none. It reports every problem on
# stderr and fails. A small parser for the front matter's block-style metadata mapping, so the
# workstation needs no YAML module.
network_annotations() {
  python3 -I - "$1" <<'PY'
import re, sys
from pathlib import Path

NETWORK, ALLOW = "skillci.dev/network", "skillci.dev/network-allow"
MODES = ("none", "allowlist", "full")
LABEL = r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?"
HOST = re.compile(r"^(?:\*\.)?%s(?:\.%s)*$" % (LABEL, LABEL))
BUNDLE = re.compile(r"^@%s$" % LABEL)
MAX_ENTRIES = 20
KEY = re.compile(r"""^\s*("(?P<dq>[^"]*)"|'(?P<sq>[^']*)'|(?P<plain>[^\s"'#][^:]*?))\s*:(?:\s+(?P<value>.*?))?\s*$""")

skill_md = Path(sys.argv[1]) / "SKILL.md"
problems = []


def scalar(number, key, raw):
    """Return the string a YAML scalar on one line stands for (a trailing comment removed)."""
    if raw.startswith('"'):
        m = re.match(r'"((?:[^"\\]|\\.)*)"\s*(?:#.*)?$', raw)
        if m:
            return re.sub(r"\\(.)", r"\1", m.group(1))
    elif raw.startswith("'"):
        m = re.match(r"'((?:[^']|'')*)'\s*(?:#.*)?$", raw)
        if m:
            return m.group(1).replace("''", "'")
    elif raw[:1] in ("@", "*", "&", "!", "%", "`", "|", ">", "[", "{"):
        problems.append("line %d: quote the value of %s: YAML does not allow a plain value "
                        "that starts with %s" % (number, key, raw[0]))
        return None
    else:
        return re.sub(r"\s+#.*$", "", raw).strip()
    problems.append("line %d: %s has an unterminated quoted value" % (number, key))
    return None


text = skill_md.read_text()
front = re.match(r"---\r?\n(.*?)\r?\n---\r?\n", text, re.S)
declared = {}
inside, indent = False, None
for number, line in enumerate(front.group(1).splitlines() if front else [], 2):
    if not line.strip() or line.lstrip().startswith("#"):
        continue
    if not line[0].isspace():
        key, _, rest = line.partition(":")
        inside, indent = key.strip() == "metadata", None
        if inside and rest.strip() and "skillci.dev/" in rest:
            problems.append("line %d: write metadata as a block mapping, one key per line" % number)
        continue
    width = len(line) - len(line.lstrip())
    indent = indent or width
    m = KEY.match(line) if inside and width == indent else None
    if not m:
        continue
    key = next(g for g in (m.group("dq"), m.group("sq"), m.group("plain")) if g is not None)
    if key in (NETWORK, ALLOW):
        if key in declared:
            problems.append("line %d: %s is declared twice" % (number, key))
        value = scalar(number, key, m.group("value") or "")
        if value is not None:
            declared[key] = (number, value)

mode = declared.get(NETWORK)
if mode and mode[1] not in MODES:
    problems.append("line %d: %s must be one of %s, got %r" % (mode[0], NETWORK, ", ".join(MODES), mode[1]))
if ALLOW in declared:
    number, value = declared[ALLOW]
    entries = [entry.strip() for entry in value.split(",")]
    if mode and mode[1] in ("none", "full"):
        problems.append("line %d: %s only applies with %s: allowlist, not %s"
                        % (number, ALLOW, NETWORK, mode[1]))
    if not any(entries):
        problems.append("line %d: %s is empty" % (number, ALLOW))
    elif "" in entries:
        problems.append("line %d: %s has an empty entry" % (number, ALLOW))
    for entry in entries:
        if entry and not BUNDLE.match(entry) and not (len(entry) <= 253 and HOST.match(entry)):
            problems.append("line %d: %s entry %r is neither a lowercase hostname (optionally "
                            "*.<domain>) nor a @bundle" % (number, ALLOW, entry))
    if len(entries) > MAX_ENTRIES:
        problems.append("line %d: %s has %d entries, at most %d"
                        % (number, ALLOW, len(entries), MAX_ENTRIES))
    if len(set(entries)) != len(entries):
        problems.append("line %d: %s lists an entry twice" % (number, ALLOW))
    declared[ALLOW] = (number, ",".join(entries))

for problem in problems:
    print("%s %s" % (skill_md, problem), file=sys.stderr)
if problems:
    sys.exit(1)
for key in (NETWORK, ALLOW):
    if key in declared:
        print("%s=%s" % (key, declared[key][1]))
PY
}

if [[ "${1-}" == "--network-annotations" ]]; then
  [[ $# -eq 2 ]] || die "usage: push-skill.sh --network-annotations <dir>"
  [[ -f "$2/SKILL.md" ]] || die "no SKILL.md in $2"
  need python3
  network_annotations "$2"
  exit 0
fi

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

# Checked before anything reaches the cluster: a bad value fails here, not in the controller.
annotations="$(network_annotations "${src}")" || die "fix the network metadata of ${src}/SKILL.md"
annotation_flags=()
while IFS= read -r annotation; do
  [[ -n "${annotation}" ]] && annotation_flags+=(--annotation "${annotation}")
done <<<"${annotations}"

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
[[ -z "${annotations}" ]] || note "annotations: ${annotations//$'\n'/ }"
# The ${a[@]+...} form keeps an empty array working under set -u in bash 3.2 (macOS).
tb sh -c 'cd /workspace && oras push "$@"' sh \
  "${ORAS_FLAGS[@]}" \
  ${annotation_flags[@]+"${annotation_flags[@]}"} \
  "${HARBOR_HOST}/${HARBOR_PROJECT}/${skill}:${version}" \
  --artifact-type "${ARTIFACT_TYPE}" "${skill}/" </dev/null

note "Harbor now fires PUSH_ARTIFACT at the controller for ${skill}:${version}."
