#!/usr/bin/env bash
# Install the agent CLIs in the sandbox image at pinned releases, the way Harbor does it in a
# trial (as root, HOME=/root), so that Harbor finds them and skips its own install step:
#   claude-code  native installer into ~/.local/bin   (harbor/agents/installed/claude_code.py)
#   codex        npm @openai/codex under nvm Node 22  (codex.py), node and codex linked into /usr/local/bin
#   opencode     npm opencode-ai under nvm Node 22    (opencode.py)
# Harbor runs commands in `bash -ic` and sources ~/.nvm/nvm.sh, so nvm is set up in ~/.bashrc.
# Run by sandbox/Dockerfile with NVM_VERSION, NODE_RELEASE, CLAUDE_CODE_VERSION, CODEX_VERSION
# and OPENCODE_VERSION in the environment.
set -euo pipefail

: "${NVM_VERSION:?}" "${NODE_RELEASE:?}" "${CLAUDE_CODE_VERSION:?}" "${CODEX_VERSION:?}" "${OPENCODE_VERSION:?}"
export HOME=/root
cd "${HOME}"
touch .bashrc # nvm's installer only edits a profile that exists

curl -fsSL https://downloads.claude.ai/claude-code-releases/bootstrap.sh | bash -s -- "${CLAUDE_CODE_VERSION}"
# shellcheck disable=SC2016 # the line is written to .bashrc unexpanded
echo 'export PATH="$HOME/.local/bin:$PATH"' >>.bashrc
export PATH="${HOME}/.local/bin:${PATH}"

# nvm's installer installs $NODE_VERSION when it is set; Harbor unsets it for the same reason.
curl -fsSL "https://raw.githubusercontent.com/nvm-sh/nvm/${NVM_VERSION}/install.sh" | env -u NODE_VERSION bash
export NVM_DIR="${HOME}/.nvm"
# shellcheck source=/dev/null
. "${NVM_DIR}/nvm.sh"
nvm install "${NODE_RELEASE}"
nvm alias default "${NODE_RELEASE}"
npm install -g "@openai/codex@${CODEX_VERSION}" "opencode-ai@${OPENCODE_VERSION}"

for bin in node codex opencode; do
  ln -sf "$(command -v "${bin}")" "/usr/local/bin/${bin}"
done

# Fail the image build, not the first trial, when a release is not the pinned one.
claude --version | grep -Fq "${CLAUDE_CODE_VERSION}"
codex --version | grep -Fq "${CODEX_VERSION}"
opencode --version | grep -Fq "${OPENCODE_VERSION}"
node --version | grep -Fq "v${NODE_RELEASE}"

npm cache clean --force
nvm cache clear
