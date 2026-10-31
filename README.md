# skill-ci

skill-ci is continuous integration for agent skills on Kubernetes. You publish a skill as an OCI artifact to Harbor (goharbor), and skill-ci evaluates every new version against a list of agents and models, with and without the skill, using NVIDIA SkillEvaluator. The results decide which version carries the `beta` and `stable` tags in the registry, so whoever pulls `stable` gets a version that measurably helps and has not regressed against the stable version before it.

What a skill does for an agent depends on the agent, its release and the model, and one run is noisy: the same skill version scored 0.95 on one run and 0.87 on the next. A new version can also score higher than no skill at all while being worse than the current stable version. skill-ci runs every trial in its own Agent Sandbox pod, keeps the per-trial scores, and promotes a version only when the confidence interval of its lift is above zero on every required model.

## How it works

1. `oras push` publishes a skill version (`vX.Y.Z`) to the Harbor project `skills`. Harbor sends a `PUSH_ARTIFACT` webhook to the skill-ci controller.
2. The controller creates one `SkillEval` per skill digest and model list entry (agent, agent version, model), resolves the network mode and sandbox profile, and starts a runner Job for each.
3. The runner pulls the skill with ORAS and runs SkillEvaluator: Tier 1 static and security checks and Tier 2 deduplication first, then the live Tier 3 trials with and without the skill, each trial in an Agent Sandbox from a warm pool.
4. The runner attaches the SkillEvaluator report to the skill digest in Harbor and hands the scores back to the controller.
5. The controller computes the lift and its confidence interval, applies the tag policy (`beta`, `stable`, regression gate) through the Harbor API, and exports metrics.
6. A change to the model list (a new model, a new agent release) evaluates every `beta` and `stable` version again.

[docs/architecture.md](docs/architecture.md) describes the parts and the flow in more detail.

## Requirements

skill-ci runs on any conformant Kubernetes cluster. The Pulumi program in [infra/](infra/) creates a reference cluster on DigitalOcean (DOKS), but nothing in the controller, the runner or the chart depends on DigitalOcean.

- Kubernetes 1.30 or newer for the full install in [deploy/README.md](deploy/README.md). The controller chart itself declares `kubeVersion: >=1.27.0-0`. The reference DOKS cluster runs the newest 1.36 patch release.
- [Agent Sandbox](https://github.com/kubernetes-sigs/agent-sandbox) v1.0.9, CRDs and controller, installed from its `sandbox-with-extensions.yaml` release manifest. Without the extensions CRDs (`SandboxTemplate`, `SandboxWarmPool`, `SandboxClaim`) every trial starts a bare `Sandbox` instead of a warm pod.
- A Harbor (goharbor) registry that the controller and the runner Jobs can reach, with a project `skills` and a `PUSH_ARTIFACT` webhook policy pointing at the controller. The defaults assume Harbor in the cluster at `http://harbor.harbor.svc.cluster.local` (`controller.harborUrl`). [deploy/README.md](deploy/README.md) installs Harbor chart 1.19.4 and a Job that creates the project and the webhook.
- In the controller namespace: the Secrets `harbor-credentials`, `harbor-pull`, `harbor-webhook` (optional) and `llm-keys`, and the model list ConfigMap `skill-ci-models`. `llm-keys` needs at least the key `anthropic`, because Anthropic is the judge of every run. Keys for OpenAI, OpenRouter, Ollama Cloud and NVIDIA are optional and add Codex and OpenCode models.
- Outbound internet access from the nodes to GHCR and the LLM APIs. All images are public on GHCR, and nothing builds images in the cluster.
- Optional: [Cilium](https://cilium.io) as the CNI, for the network modes `none` and `allowlist`. Without it only `full` runs (the internet, never the cluster), and a skill that asks for another mode fails with a message that says so. See [CONTRACT.md, "Network modes"](CONTRACT.md).
- Optional: the Prometheus Operator CRDs for the chart's `ServiceMonitor`, and any OTLP backend for the OpenTelemetry export.

If you install Harbor and Prometheus in the cluster as deploy/README.md does, you also need a default StorageClass with `ReadWriteOnce` volumes and room for several sandbox pods of 1 vCPU and 2 GiB each. [deploy/README.md](deploy/README.md) lists sizes and platform notes for DOKS, EKS, GKE, AKS, kind and minikube.

## Quick start

Install everything (Agent Sandbox, Harbor, Secrets, model list, optional Prometheus and Grafana, the controller) on the cluster in your current `KUBECONFIG`:

```bash
git clone https://github.com/dirien/skill-ci.git
cd skill-ci
export ANTHROPIC_API_KEY=sk-ant-...      # required
deploy/install.sh
make seed                                # push the sample skills as v1.0.0
make status                              # SkillEvals and the tags per skill in Harbor
```

If Harbor, Agent Sandbox, the Secrets and the model list are already in place, install only the controller from the OCI chart:

```bash
helm install skill-ci oci://ghcr.io/dirien/charts/skill-ci-controller --version 0.10.1 -n skill-ci --create-namespace
kubectl -n skill-ci get skillevals
```

[deploy/README.md](deploy/README.md) walks through every step of the install with expected output, OpenTelemetry settings, troubleshooting and teardown. [docs/reference-stack.md](docs/reference-stack.md) describes the Pulumi stack in [infra/](infra/) that builds a DOKS cluster with all of the above.

## Versions

| Part | Role | Version |
| --- | --- | --- |
| skill-ci controller | Receives pushes, watches the model list, runs SkillEvals as Jobs, moves `beta` and `stable` | 0.10.1 (chart `charts/skill-ci-controller`) |
| skill-ci runner | One Job per SkillEval: ORAS pull, SkillEvaluator Tier 1, 2 and 3, per-trial scores, report upload | 0.10.1 |
| skill-ci sandbox image | The image every trial sandbox starts from: Python, the verifier packages, and the Claude Code, Codex and OpenCode CLIs at their pins | 0.10.1 |
| [goharbor](https://goharbor.io) | Stores skills as OCI artifacts, sends `PUSH_ARTIFACT` webhooks | 2.15.4 (chart 1.19.4) |
| [ORAS](https://oras.land) | Pushes, pulls and tags skills | 1.3.0 |
| [NVIDIA SkillEvaluator](https://github.com/NVIDIA/SkillEvaluator) | Live with/without-skill runs, lift scoring | 0.6.0 + [patch](skillevaluator-patch/) |
| [Harbor framework](https://harborframework.com) | Runs agent trials | 0.24.0 (pinned by SkillEvaluator) |
| [Agent Sandbox](https://github.com/kubernetes-sigs/agent-sandbox) | One sandbox per trial, warm pools | v1.0.9 |
| [kube-prometheus-stack](https://github.com/prometheus-community/helm-charts) | Prometheus and Grafana for the controller metrics (optional) | chart 92.10.1 |

The controller, runner, sandbox image and chart always share one version. [deploy/versions.env](deploy/versions.env) is the single source of the versions for the install path, and `deploy/check-versions.sh` checks that `infra/Pulumi.yaml` uses the same ones.

## Documentation

| Document | Contents |
| --- | --- |
| [docs/architecture.md](docs/architecture.md) | The parts, the flow from push to tag, triggers, eval reports, the sandbox image |
| [docs/agents-and-models.md](docs/agents-and-models.md) | The model list, agent releases, required and optional entries, LLM keys |
| [docs/gate.md](docs/gate.md) | SkillEvaluator tiers and scanners, the statistical gate, the tag policy |
| [docs/network-and-sandboxes.md](docs/network-and-sandboxes.md) | Network modes, `SkillCIConfig`, `SandboxProfile`, warm pools, sandbox sizing |
| [docs/observability.md](docs/observability.md) | SkillEval status, the reporting site, eval reports, Prometheus metrics, the Grafana dashboard, OTLP export |
| [docs/sample-skills.md](docs/sample-skills.md) | The sample skills and a walkthrough of the loop with the `make` targets |
| [docs/reference-stack.md](docs/reference-stack.md) | The Pulumi reference stack on DOKS in `infra/` |
| [docs/releasing.md](docs/releasing.md) | Releases, published artifacts, the Helm chart, CI |
| [docs/design-notes.md](docs/design-notes.md) | Why things are built the way they are |
| [CONTRACT.md](CONTRACT.md) | The specification: names, CRDs, the runner Job contract, statistics, policy, metrics |
| [deploy/README.md](deploy/README.md) | Step-by-step install on your own cluster with Helm and kubectl |
| [charts/skill-ci-controller/README.md](charts/skill-ci-controller/README.md) | Chart values, CRD handling, adopting an existing install |

## Repository layout

```text
controller/            The skill-ci controller (Go)
runner/                In-cluster runner image and the SkillEval Job (ORAS init container + SkillEvaluator)
sandbox/               Dockerfile of the trial sandbox image (Python, verifier packages, agent CLIs) and the script that installs the CLIs
harbor-agent-sandbox/  Harbor framework environment that runs each trial as an agents.x-k8s.io Sandbox from the sandbox image
skillevaluator-patch/  Patch for SkillEvaluator 0.6.0: that environment, its sandbox image and sizing, a Tier 2 catalog for validate, the agent CLI version, OpenCode behind an OpenAI-compatible gateway
charts/                Helm chart of the controller, published as oci://ghcr.io/dirien/charts/skill-ci-controller
config/                Controller manifests (CRD source, RBAC, Deployment, Service), plain `kubectl apply -k` alternative
deploy/                Install path with Helm and kubectl: install and uninstall scripts, manifests, values, versions.env
infra/                 Pulumi YAML reference stack: DOKS, Agent Sandbox, goharbor, secrets, Harbor setup Job, toolbox, model list, Prometheus + Grafana
infra/dashboards/      Grafana dashboard JSON (provisioned through a ConfigMap)
skills/                Sample skills with evals
demo/                  Shell scripts behind the Makefile targets, skill version variants, initial model list
tools/                 Generator of the offline report page (`make report-page`)
reports/               Eval reports downloaded by `make report`
.github/               CI and release workflows, Dependabot
.goreleaser.yaml       Controller binaries and image of a release
```

`make help` lists the Makefile targets.
