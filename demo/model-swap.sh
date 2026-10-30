#!/usr/bin/env bash
# Change the model list live. The controller watches the ConfigMap, evaluates every beta/stable
# skill digest against each entry that was added (an entry is agent + agentVersion + model, so a
# new agent version counts as added), and applies a changed `required` flag to the existing runs.
#   demo/model-swap.sh add            add claude-code claude-sonnet-5-5 as an optional model (default)
#   demo/model-swap.sh swap           replace claude-opus-5-5 with claude-sonnet-5-5 (required)
#   demo/model-swap.sh bump [VERSION] the harness changed: move Claude Code to VERSION (default 2.1.308,
#                                     the npm "stable" tag); every beta/stable version runs again on it
#   demo/model-swap.sh require AGENT  make every entry of AGENT (claude-code, codex, opencode) gate promotion
#   demo/model-swap.sh optional AGENT stop gating on every entry of AGENT
#   demo/model-swap.sh restore        back to demo/models.yaml
# add, swap and restore start from demo/models.yaml; bump, require and optional start from the list in
# the cluster, so they combine with each other.
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

mode="${1:-add}"
arg="${2-}"
base="${DEMO_DIR}/models.yaml"
need kubectl
need python3

# edit MODE ARG reads a model list on stdin and writes the changed list to stdout. The lists are
# written by hand in one layout ("  - agent: x" starts an entry), so the edit works on lines.
edit() {
  python3 -I -c '
import re, sys

mode, arg = sys.argv[1], sys.argv[2]
lines = sys.stdin.read().splitlines()
out, current_agent, changed = [], None, 0
for line in lines:
    m = re.match(r"^\s*-\s+agent:\s*(\S+)", line)
    if m:
        current_agent = m.group(1)
    if mode == "bump" and current_agent == "claude-code" and re.match(r"^\s+agentVersion:", line):
        line, changed = re.sub(r"(agentVersion:\s*).*", lambda g: g.group(1) + "\"" + arg + "\"", line), changed + 1
    elif mode in ("require", "optional") and current_agent == arg and re.match(r"^\s+required:", line):
        flag = "true" if mode == "require" else "false"
        line, changed = re.sub(r"(required:\s*).*", lambda g: g.group(1) + flag, line), changed + 1
    elif mode == "swap" and re.match(r"^\s+model:\s*claude-opus-5-5\s*$", line):
        line, changed = line.replace("claude-opus-5-5", "claude-sonnet-5-5"), changed + 1
    out.append(line)
if changed == 0 and mode in ("bump", "require", "optional", "swap"):
    sys.exit("nothing to change for " + mode + " " + arg)
print("\n".join(out))
' "$1" "$2"
}

current() {
  kubectl -n "${NS}" get configmap skill-ci-models -o go-template='{{index .data "models.yaml"}}'
}

case "${mode}" in
add)
  models="$(cat "${base}"
    printf '  - agent: claude-code\n    agentVersion: "2.1.292"\n    model: claude-sonnet-5-5\n    required: false\n')"
  ;;
swap)
  models="$(edit swap "" <"${base}")"
  ;;
bump)
  version="${arg:-2.1.308}"
  [[ "${version}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*[A-Za-z0-9]$ ]] || die "'${version}' is not a version like 2.1.308"
  models="$(current | edit bump "${version}")"
  ;;
require | optional)
  case "${arg}" in
  claude-code | codex | opencode) ;;
  *) die "usage: model-swap.sh ${mode} claude-code|codex|opencode" ;;
  esac
  models="$(current | edit "${mode}" "${arg}")"
  ;;
restore)
  models="$(cat "${base}")"
  ;;
*)
  die "unknown mode '${mode}'. Use add, swap, bump, require, optional, or restore."
  ;;
esac

say "Model list (${mode}${arg:+ ${arg}})"
printf '%s\n' "${models}" | sed 's/^/    /'

patch="$(printf '%s' "${models}" | python3 -I -c 'import json,sys; print(json.dumps({"data": {"models.yaml": sys.stdin.read() + "\n"}}))')"
kubectl -n "${NS}" patch configmap skill-ci-models --type merge -p "${patch}"
note "ConfigMap skill-ci-models updated. The controller picks up the change on its own."
