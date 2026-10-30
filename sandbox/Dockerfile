# The generic sandbox image of skill-ci: one image for every trial, built once per release.
#
# SkillEvaluator writes a Dockerfile for each task. Apart from its COPY, ENV and WORKDIR lines
# (replayed when a sandbox starts, see harbor-agent-sandbox/harbor_agent_sandbox/dockerfile.py)
# every task of a plain skill is the same image, so it is built here once instead of in the
# cluster for each task. On top of that image the three agent CLIs are installed at the pinned
# releases, so a trial does not install them (Harbor skips the install when the CLI is there).
#
# Build context: sandbox/. Published as ghcr.io/dirien/skill-ci-sandbox:<release> (linux/amd64).
FROM python:3.15.0-slim-bookworm

ARG NVM_VERSION=v0.40.2
ARG NODE_RELEASE=22.23.3
ARG CLAUDE_CODE_VERSION=2.1.292
ARG CODEX_VERSION=0.161.0
ARG OPENCODE_VERSION=1.19.1

# What the task Dockerfile installs (bash curl git jq ripgrep), plus what the agent installs
# bring along in a trial (ca-certificates, coreutils for stdbuf, procps for pgrep).
RUN apt-get -o Acquire::Retries=3 update && \
    apt-get -o Acquire::Retries=3 install -y --no-install-recommends \
    bash ca-certificates coreutils curl git jq procps ripgrep \
    && rm -rf /var/lib/apt/lists/*

# The verifier packages and the import smoke test, copied verbatim from the Dockerfile that
# SkillEvaluator 0.6.0 generates (harbor-agent-sandbox/tests/fixtures). A test compares them.
RUN ["/bin/sh", "-c", "PATH=\"${PATH:+$PATH:}/usr/local/bin:/usr/bin:/bin\"; export PATH; exec \"$@\"", "skillevaluator", "python", "-m", "pip", "install", "--no-cache-dir", "ragas~=0.4.0", "langchain-community<0.4.2", "openai>=1.0", "anthropic>=0.40", "boto3>=1.34", "idna>=3.10,<4"]
RUN ["/bin/sh", "-c", "PATH=\"${PATH:+$PATH:}/usr/local/bin:/usr/bin:/bin\"; export PATH; exec \"$@\"", "skillevaluator", "python", "-c", "from ragas import SingleTurnSample; from ragas.llms.base import llm_factory; from ragas.messages import AIMessage, HumanMessage; from ragas.metrics.collections import AgentGoalAccuracyWithReference"]

RUN mkdir -p /workspace/skills /workspace/input /workspace/output \
    /logs/verifier /logs/agent

# Claude Code, Codex and OpenCode, installed the way Harbor installs them.
COPY install-agents.sh /tmp/install-agents.sh
RUN NVM_VERSION="${NVM_VERSION}" NODE_RELEASE="${NODE_RELEASE}" \
    CLAUDE_CODE_VERSION="${CLAUDE_CODE_VERSION}" CODEX_VERSION="${CODEX_VERSION}" \
    OPENCODE_VERSION="${OPENCODE_VERSION}" bash /tmp/install-agents.sh \
    && rm /tmp/install-agents.sh

# Claude Code must keep the pinned release for the whole trial.
ENV DISABLE_AUTOUPDATER=1

LABEL org.opencontainers.image.title="skill-ci-sandbox" \
      org.opencontainers.image.description="Generic trial sandbox of skill-ci: Python 3.12, the verifier packages and the Claude Code, Codex and OpenCode CLIs" \
      org.opencontainers.image.source="https://github.com/dirien/skill-ci" \
      dev.skill-ci.agents="claude-code=${CLAUDE_CODE_VERSION} codex=${CODEX_VERSION} opencode=${OPENCODE_VERSION}"

# The Sandbox sets its own command; this keeps `docker run` usable.
CMD ["sleep", "infinity"]
