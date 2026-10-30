# Runs one SkillEval: ORAS pulls the skill, SkillEvaluator's live tier scores it,
# trials run as Agent Sandboxes. Build context is the repo root.
FROM python:3.12.16-slim-bookworm

ARG SKILLEVALUATOR_VERSION=v0.6.0
ARG BUILDKIT_VERSION=v0.34.1
ARG BUILDKIT_SHA256=a59539daf547fe0958efe2d07e41c0a6944e1112a66d7e519ec1cf5b673ef15b
ARG ORAS_VERSION=1.3.0
ARG ORAS_SHA256=6cdc692f929100feb08aa8de584d02f7bcc30ec7d88bc2adc2054d782db57c64
ARG KUBERNETES_VERSION=37.0.1

# Nodes are linux/amd64.
RUN apt-get update && apt-get install -y --no-install-recommends git curl patch ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && curl -sSfL -o /tmp/buildkit.tgz https://github.com/moby/buildkit/releases/download/${BUILDKIT_VERSION}/buildkit-${BUILDKIT_VERSION}.linux-amd64.tar.gz \
 && echo "${BUILDKIT_SHA256}  /tmp/buildkit.tgz" | sha256sum -c - \
 && tar xzf /tmp/buildkit.tgz -C /usr/local bin/buildctl \
 && curl -sSfL -o /tmp/oras.tgz https://github.com/oras-project/oras/releases/download/v${ORAS_VERSION}/oras_${ORAS_VERSION}_linux_amd64.tar.gz \
 && echo "${ORAS_SHA256}  /tmp/oras.tgz" | sha256sum -c - \
 && tar xzf /tmp/oras.tgz -C /usr/local/bin oras \
 && rm /tmp/buildkit.tgz /tmp/oras.tgz

COPY skillevaluator-patch/skillevaluator-0.6.0-ack-import-path.patch /tmp/
RUN git clone --depth 1 --branch ${SKILLEVALUATOR_VERSION} https://github.com/NVIDIA/SkillEvaluator /opt/skillevaluator \
 && cd /opt/skillevaluator && patch -p1 < /tmp/skillevaluator-0.6.0-ack-import-path.patch \
 && pip install --no-cache-dir "/opt/skillevaluator[tier3]"

COPY harbor-agent-sandbox /opt/harbor-agent-sandbox
RUN pip install --no-cache-dir /opt/harbor-agent-sandbox kubernetes==${KUBERNETES_VERSION}

COPY runner/skill_eval.py /usr/local/bin/skill_eval.py

# Runner defaults (CONTRACT.md); the Job may override any of them.
ENV PYTHONUNBUFFERED=1 \
    HOME=/work \
    REGISTRY_CONFIG=/auth/config.json \
    REGISTRY_PLAIN_HTTP=true \
    SANDBOX_NAMESPACE=skill-ci \
    TASK_IMAGE_REGISTRY=registry.digitalocean.com/skill-ci-dirien \
    TASK_IMAGE_PULL_SECRET=docr \
    SKILLEVALUATOR_ACK_ENV_IMPORT_PATH=harbor_agent_sandbox:AgentSandboxEnvironment \
    HARBOR_BUILDKIT_ADDRESS=tcp://buildkitd.skill-ci:1234

# HOME must be writable whatever user the Job runs as.
RUN mkdir -p /work && chmod 1777 /work
WORKDIR /work
ENTRYPOINT ["python", "/usr/local/bin/skill_eval.py"]
