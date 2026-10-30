# Skills Are Packages Now: test stack

Proof that the skill CI loop from the talk runs on generic Kubernetes (DOKS), built from open source parts:

| Part | Role | Version |
|---|---|---|
| [goharbor](https://goharbor.io) | Stores skills as OCI artifacts, fires `PUSH_ARTIFACT` webhooks | 2.15.4 (chart 1.19.4) |
| [ORAS](https://oras.land) | Pushes, pulls and tags skills | 1.3.0 |
| [NVIDIA SkillEvaluator](https://github.com/NVIDIA/SkillEvaluator) | Live with/without-skill runs, lift scoring | 0.6.0 + [patch](skillevaluator-patch/) |
| [Harbor framework](https://harborframework.com) | Runs agent trials | 0.24.0 (pinned by SkillEvaluator) |
| [Agent Sandbox](https://github.com/kubernetes-sigs/agent-sandbox) | One `Sandbox` per trial | v1.0.9 |
| [BuildKit](https://github.com/moby/buildkit) | Builds task images in the cluster | v0.34.1 |
| DOKS + DOCR | Cluster and image registry | Kubernetes 1.36.5 |

## Layout

```
infra/                 Pulumi YAML: DOKS, DOCR, Agent Sandbox, BuildKit, goharbor, webhook receiver
harbor-agent-sandbox/  Harbor environment that runs each trial as an agents.x-k8s.io Sandbox
skillevaluator-patch/  Two-hunk patch so SkillEvaluator can use that environment
runner/                In-cluster runner image and the SkillEval Job (ORAS init container + SkillEvaluator)
```

## What was tested (2026-10-30)

1. **Trials in Agent Sandbox on DOKS.** SkillEvaluator ran from a Kubernetes Job with in-cluster credentials. Each trial was an Agent Sandbox; BuildKit built the task images into DOCR; no sandboxes were left behind.
2. **Model matrix.** Same skill, three pinned models, three parallel Jobs:

   | Model | With skill | Without | Lift | Job time |
   |---|---|---|---|---|
   | claude-haiku-5-5 | 0.95 | 0.36 | +0.59 | 4m07s |
   | claude-sonnet-5-5 | 0.95 | 0.32 | +0.64 | 6m52s |
   | claude-opus-5-5 | 0.95 | 0.36 | +0.58 | 7m59s |

   The test skill (`acme-release-notes`) teaches a made-up format, so no model knows it without the skill. It proves the pipeline, not a regression. The regression has to come from the pilot with real skills.
3. **Registry trigger.** `oras push` fires `PUSH_ARTIFACT`. `oras tag` (promotion) fires `PUSH_ARTIFACT` again with the same digest. Deleting a tag through the Harbor API (demotion) fires nothing.

## Findings that shape the build

- **Harbor's generic Kubernetes backend (`ack`) is thin.** In DinD mode it ships only the Dockerfile to the build Job, so every `COPY` fails. Its context archive drops empty directories, which breaks the no-skill baseline. It has no Harbor network or resource policy support. `harbor-agent-sandbox` fixes the build context and swaps the privileged pod for an unprivileged `Sandbox`.
- **SkillEvaluator hard-codes Harbor environments** and filters the environment variables it passes to Harbor. The patch adds `SKILLEVALUATOR_ACK_ENV_IMPORT_PATH` and lets `HARBOR_BUILDKIT_ADDRESS` through. Both changes are upstream PR candidates.
- **SkillEvaluator rejects symlinked eval directories**, so skills can't be mounted from a ConfigMap. Pull them with ORAS into an `emptyDir` instead, which is the design anyway.
- **The controller must deduplicate by digest, not tag**, or every promotion triggers a new eval.

## Reproduce

```bash
# Infra (local state in ~/.pulumi-local; the sandbox proxy breaks Pulumi Cloud update tokens)
. ~/.pulumi-local/skill-ci-env.sh
cd infra && pulumi up

# Runner image (built by the cluster's BuildKit)
kubectl -n skill-ci port-forward svc/buildkitd 1234:1234 &
buildctl --addr tcp://127.0.0.1:1234 build --frontend dockerfile.v0 \
  --local context=. --local dockerfile=runner \
  --output type=image,name=registry.digitalocean.com/skill-ci-dirien/skill-ci-runner:0.1.0,push=true

# Skill + eval
oras push registry.digitalocean.com/skill-ci-dirien/skill-acme-release-notes:v1.0.0 \
  --artifact-type application/vnd.agentskill.v1 acme-release-notes/
kubectl -n skill-ci create secret generic llm-keys --from-literal=anthropic="$ANTHROPIC_API_KEY"
kubectl apply -f runner/job.yaml
kubectl -n skill-ci get sandboxes -w

# Tear down
cd infra && pulumi destroy
```
