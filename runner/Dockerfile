# Runs one SkillEval: ORAS pulls the skill, SkillEvaluator checks it through all three tiers
# (Tier 1 static and security, Tier 2 deduplication, Tier 3 live trials as Agent Sandboxes that
# run the generic sandbox image, sandbox/Dockerfile). Build context is the repo root.
FROM python:3.12.16-slim-bookworm

ARG SKILLEVALUATOR_VERSION=v0.6.0
ARG ORAS_VERSION=1.3.0
ARG ORAS_SHA256=6cdc692f929100feb08aa8de584d02f7bcc30ec7d88bc2adc2054d782db57c64
ARG KUBERNETES_VERSION=37.0.1
# Tier 1 scanners. SkillEvaluator needs all of them on PATH for trustworthy evidence: a missing or
# crashing scanner leaves the run "incomplete". Bandit and pip-audit come with its [security] extra.
ARG GITLEAKS_VERSION=8.30.1
ARG GITLEAKS_SHA256=551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb
ARG SKILLSPECTOR_VERSION=v2.12.0
ARG SKILLSPECTOR_COMMIT=c7958a3268d9498644b22edb75d0f051bbc8cbfc
ARG SEMGREP_VERSION=1.183.0
ARG BANDIT_VERSION=1.9.4
ARG PIP_AUDIT_VERSION=2.10.1

# Nodes are linux/amd64.
RUN apt-get update && apt-get install -y --no-install-recommends git curl patch ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && curl -sSfL -o /tmp/oras.tgz https://github.com/oras-project/oras/releases/download/v${ORAS_VERSION}/oras_${ORAS_VERSION}_linux_amd64.tar.gz \
 && echo "${ORAS_SHA256}  /tmp/oras.tgz" | sha256sum -c - \
 && tar xzf /tmp/oras.tgz -C /usr/local/bin oras \
 && curl -sSfL -o /tmp/gitleaks.tgz https://github.com/gitleaks/gitleaks/releases/download/v${GITLEAKS_VERSION}/gitleaks_${GITLEAKS_VERSION}_linux_x64.tar.gz \
 && echo "${GITLEAKS_SHA256}  /tmp/gitleaks.tgz" | sha256sum -c - \
 && tar xzf /tmp/gitleaks.tgz -C /usr/local/bin gitleaks \
 && rm /tmp/oras.tgz /tmp/gitleaks.tgz

# SkillSpector and Semgrep pin conflicting click versions, so each gets its own virtualenv.
# SkillEvaluator finds both on PATH. SkillSpector is pinned to the v2.12.0 commit.
RUN python -m venv /opt/skillspector \
 && /opt/skillspector/bin/pip install --no-cache-dir "git+https://github.com/NVIDIA/SkillSpector.git@${SKILLSPECTOR_COMMIT}" \
 && ln -s /opt/skillspector/bin/skillspector /usr/local/bin/skillspector \
 && python -m venv /opt/semgrep \
 && /opt/semgrep/bin/pip install --no-cache-dir "semgrep==${SEMGREP_VERSION}" \
 && ln -s /opt/semgrep/bin/semgrep /usr/local/bin/semgrep

COPY skillevaluator-patch/skillevaluator-0.6.0-skill-ci.patch /tmp/
RUN git clone --depth 1 --branch ${SKILLEVALUATOR_VERSION} https://github.com/NVIDIA/SkillEvaluator /opt/skillevaluator \
 && cd /opt/skillevaluator && patch -p1 < /tmp/skillevaluator-0.6.0-skill-ci.patch \
 && pip install --no-cache-dir "/opt/skillevaluator[tier3,security]" \
      "bandit==${BANDIT_VERSION}" "pip-audit==${PIP_AUDIT_VERSION}"

COPY harbor-agent-sandbox /opt/harbor-agent-sandbox
RUN pip install --no-cache-dir /opt/harbor-agent-sandbox kubernetes==${KUBERNETES_VERSION}

# Fail the build, not the first run, when a tool is missing or the wrong version.
RUN gitleaks version | grep -qx "${GITLEAKS_VERSION}" \
 && skillspector --version | grep -q "${SKILLSPECTOR_VERSION#v}" \
 && semgrep --version | grep -qx "${SEMGREP_VERSION}" \
 && bandit --version | grep -q "${BANDIT_VERSION}" \
 && skillevaluator --version \
 && python -c "from skillevaluator.tier3.harbor import runner as r; \
assert r._GATEWAY_OPENCODE_IMPORT_PATH in r._NATIVE_BACKEND_AGENT_IMPORT_PATHS, 'patch hunk 6: OpenCode gateway not allowed on ack'"

COPY runner/skill_eval.py /usr/local/bin/skill_eval.py

# The release this image belongs to: the release workflow passes the tag, and the runner runs
# its trials in the sandbox image of the same release.
ARG SKILL_CI_VERSION=latest

# Runner defaults (CONTRACT.md); the Job may override any of them.
ENV PYTHONUNBUFFERED=1 \
    HOME=/work \
    REGISTRY_CONFIG=/auth/config.json \
    REGISTRY_PLAIN_HTTP=true \
    SANDBOX_NAMESPACE=skill-ci \
    SANDBOX_IMAGE=ghcr.io/dirien/skill-ci-sandbox:${SKILL_CI_VERSION} \
    SKILLEVALUATOR_ACK_ENV_IMPORT_PATH=harbor_agent_sandbox:AgentSandboxEnvironment

# HOME must be writable whatever user the Job runs as.
RUN mkdir -p /work && chmod 1777 /work
WORKDIR /work
ENTRYPOINT ["python", "/usr/local/bin/skill_eval.py"]
