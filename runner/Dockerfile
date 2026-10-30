# Runs SkillEvaluator's live tier inside the cluster, with trials in Agent Sandbox.
FROM python:3.12-slim

ARG SKILLEVALUATOR_VERSION=v0.6.0
ARG BUILDKIT_VERSION=v0.34.1

RUN apt-get update && apt-get install -y --no-install-recommends git curl patch ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && curl -sSL https://github.com/moby/buildkit/releases/download/${BUILDKIT_VERSION}/buildkit-${BUILDKIT_VERSION}.linux-amd64.tar.gz \
    | tar xz -C /usr/local --strip-components=0 bin/buildctl

COPY skillevaluator-patch/skillevaluator-0.6.0-ack-import-path.patch /tmp/
RUN git clone --depth 1 --branch ${SKILLEVALUATOR_VERSION} https://github.com/NVIDIA/SkillEvaluator /opt/skillevaluator \
 && cd /opt/skillevaluator && patch -p1 < /tmp/skillevaluator-0.6.0-ack-import-path.patch \
 && pip install --no-cache-dir "/opt/skillevaluator[tier3]"

COPY harbor-agent-sandbox /opt/harbor-agent-sandbox
RUN pip install --no-cache-dir /opt/harbor-agent-sandbox

ENV SKILLEVALUATOR_ACK_ENV_IMPORT_PATH=harbor_agent_sandbox:AgentSandboxEnvironment \
    HARBOR_BUILDKIT_ADDRESS=tcp://buildkitd.skill-ci:1234
WORKDIR /work
ENTRYPOINT ["skillevaluator"]
