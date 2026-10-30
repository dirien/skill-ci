# Skills Are Packages Now: test stack

Proof that the skill CI loop from the talk runs on generic Kubernetes (DOKS), built from open source parts:

| Part | Role | Version |
| --- | --- | --- |
| [goharbor](https://goharbor.io) | Stores skills as OCI artifacts, fires `PUSH_ARTIFACT` webhooks | 2.15.4 (chart 1.19.4) |
| [ORAS](https://oras.land) | Pushes, pulls and tags skills | 1.3.0 |
| [NVIDIA SkillEvaluator](https://github.com/NVIDIA/SkillEvaluator) | Live with/without-skill runs, lift scoring | 0.6.0 + [patch](skillevaluator-patch/) |
| [Harbor framework](https://harborframework.com) | Runs agent trials | 0.24.0 (pinned by SkillEvaluator) |
| [Agent Sandbox](https://github.com/kubernetes-sigs/agent-sandbox) | One `Sandbox` per trial | v1.0.9 |
| [BuildKit](https://github.com/moby/buildkit) | Builds task images in the cluster | v0.34.1 |
| DOKS + DOCR | Cluster and image registry (DOCR professional tier) | Kubernetes 1.36.5 |
| [kube-prometheus-stack](https://github.com/prometheus-community/helm-charts) | Prometheus and Grafana for the controller metrics | chart 92.10.1 |

## Layout

```text
infra/                 Pulumi YAML: DOKS, DOCR, Agent Sandbox, BuildKit, goharbor, secrets, Harbor setup Job, toolbox, model list, Prometheus + Grafana
infra/dashboards/      Grafana dashboard JSON (provisioned through a ConfigMap)
skills/                Sample skills with evals (demo material, see below)
demo/                  Shell scripts behind the Makefile targets, skill version variants, initial model list
controller/            The skill-ci controller (Go)
config/                Controller manifests (CRD, RBAC, Deployment, Service), applied with kubectl apply -k
harbor-agent-sandbox/  Harbor environment that runs each trial as an agents.x-k8s.io Sandbox
skillevaluator-patch/  Two-hunk patch so SkillEvaluator can use that environment
runner/                In-cluster runner image and the SkillEval Job (ORAS init container + SkillEvaluator)
```

## Run the demo

Everything goes through `make` (`make help` lists the targets). Scripts print what they do in plain words.

| Target | What it does |
| --- | --- |
| `make up` | `pulumi up`: cluster, registry, Harbor, secrets, project and webhook, toolbox, model list, Prometheus and Grafana |
| `make docr-login` | Writes DOCR push credentials from the cluster to `~/.cache/skill-ci/docker` (used by `make build` when present) |
| `make build` | Builds the controller and runner images in the cluster's BuildKit and pushes them to DOCR (`TAG=0.1.0`, `BUILDCTL=buildctl`, `DOCKER_CONFIG=...`) |
| `make deploy` | `kubectl apply -k config/`, then sets the controller and runner image tags |
| `make seed` | Pushes every skill under `skills/` as `v1.0.0` through the toolbox pod. Skips tags that exist. `RETRIGGER=1` re-fires the push webhook for existing tags |
| `make demo-push` | Pushes a new version of a skill (`SKILL=acme-release-notes VERSION=v1.1.0`) |
| `make demo-model-swap` | Changes the model list live (`MODE=add` adds `claude-code/claude-opus-5-5`, `swap` replaces sonnet with opus, `restore` resets) |
| `make status` | `kubectl get skillevals -o wide` plus tags and digests per skill from the Harbor API |
| `make dashboard` | Port-forwards Grafana to `localhost:3000` and prints the login hint |
| `make reset` | Deletes SkillEvals and Jobs, removes `beta`/`stable` tags and demo versions from Harbor, restores the model list |
| `make down` | Refuses on purpose. Destroying the stack deletes the cluster, run `pulumi destroy` by hand |
| `make lint` | shellcheck, yamllint, markdownlint, JSON checks and the dataset check of the skills |

Suggested order on a fresh checkout: `make up`, `make build`, `make deploy`, `make seed RETRIGGER=1`, `make status`. The retrigger matters when the skills were pushed before the controller existed, because Harbor retries a failed webhook delivery only a few times.

Demo beats after that:

1. **Push a better version.** `make demo-push` publishes `acme-release-notes:v1.1.0`. The controller evaluates it against every model on the list.
2. **Push a regression.** `make demo-push VERSION=v1.2.0` publishes a version whose rules contradict its own evals. See below.
3. **Change the model list.** `make demo-model-swap` adds a model. The controller evaluates the current `beta`/`stable` digests against it.
4. **Show the metrics.** `make dashboard`, then open the `skill-ci` dashboard.

## Infrastructure notes

- **Harbor** has `externalURL: http://harbor.harbor.svc.cluster.local`, so pods can reach its token service. From outside the cluster, reach it only through in-cluster tooling: the toolbox pod or `kubectl -n harbor port-forward svc/harbor`.
- **Setup Job.** `harbor-setup-<id>` waits for Harbor, creates the private project `skills` if it is missing, and creates or updates the webhook policy `skill-ci` (`PUSH_ARTIFACT`, payload `Default`, `Authorization: Bearer <harbor-webhook token>`) pointing at the controller Service. It also removes policies that still point at the old debug receiver. The script is `infra/harbor-setup.sh`. The Job name carries a random id that changes when the script, the image, the admin password or the token change, so every relevant `pulumi up` runs a fresh, idempotent Job.
- **Secrets** in `skill-ci`: `harbor-credentials` (user `admin` and the generated admin password), `harbor-pull` (Docker config for `harbor.harbor.svc.cluster.local`, same credentials), `harbor-webhook` (random token). A project-scoped Harbor robot account is the production choice. Admin keeps the demo reproducible from one `pulumi up`. `llm-keys` is not managed by Pulumi. Create it by hand and never commit key values: `kubectl -n skill-ci create secret generic llm-keys --from-literal=anthropic="$ANTHROPIC_API_KEY"`.
- **Toolbox.** Deployment `toolbox` has two containers: `oras` (pinned image, the `harbor-pull` secret at `/auth/config.json`) and `harbor-api` (curl with the admin password in its env). The scripts copy a skill directory in with `tar` over `kubectl exec` and run `oras push --plain-http --registry-config /auth/config.json`.
- **Model list.** ConfigMap `skill-ci-models` is seeded from `demo/models.yaml` and has `ignoreChanges: [data]` in Pulumi, so demo edits survive later `pulumi up` runs. `make reset` restores it from the same file.
- **Monitoring.** `kube-prometheus-stack` in namespace `monitoring`: Prometheus (24h retention) and Grafana, with Alertmanager, node exporter, kube-state-metrics, default rules and the control-plane scrape jobs switched off. A `ServiceMonitor` in `skill-ci` scrapes Service `skill-ci-controller` (port `metrics`, label `app.kubernetes.io/name=skill-ci-controller`). The dashboard `infra/dashboards/skill-ci.json` is a ConfigMap with the `grafana_dashboard` label. It shows the lift matrix per skill version and model (`skillci_lift`), scores with and without the skill, `skillci_stable`, `skillci_obsolete` and `skillci_skillevals_total`. The Grafana admin password is generated and exported as the secret stack output `grafanaAdminPassword`.
- **Cost.** DOCR moved from `basic` to `professional` (about 5 to 20 USD a month) because SkillEvaluator creates one task image repository per eval case and arm, and `basic` allows five. Prometheus and Grafana run on the existing two nodes (requests of about 0.2 vCPU and 0.6 GiB in total). Live evals cost LLM tokens, see the cost guard flags in `CONTRACT.md`.

## Sample skills

The skills under `skills/` are demo material written for this talk, not production skills. Each has three eval cases in the agentskills.io format. The datasets were checked with SkillEvaluator's dataset loader and `skillevaluator tier1`. No live evaluation has been run on `python-docstrings` or `k8s-resource-naming`, so the outcomes below are what the design predicts, not results.

| Skill | What it encodes | Expected outcome, and why |
| --- | --- | --- |
| `acme-release-notes` | A made-up release note format (`ACME-RN/2`) | Helps. No model can know the format without the skill. The earlier test run on a one-case version of this skill measured +0.58 to +0.64 lift on three models |
| `k8s-resource-naming` | A team-specific naming and labelling convention for Kubernetes resources | Expected to help. The conventions (`prd` instead of `prod`, `-cfg` and `-sec` suffixes, `acme.io/*` labels) are arbitrary, so a model cannot guess them. Models may get some assertions right by chance (lowercase names, a namespace) |
| `python-docstrings` | Google-style docstrings | Candidate for Obsolete. Strong models already write Google-style docstrings with `Args:`, `Returns:` and `Raises:` unprompted. If the baseline scores at least 0.80 and the lift stays under +0.05, the policy flags it Obsolete. It could also turn out Neutral or Helps, which is why this is a candidate and not a promise |

Versions of `acme-release-notes` for the push beat live in `demo/variants/acme-release-notes/`:

- `v1.1.0` adds two rules (sort bullets by ticket ID, `- None [ACME-0]` for an empty section) and matching assertions. Expected to stay Helps and take over `stable`.
- `v1.2.0` is a deliberate regression. Its rules (a `# Release` heading, friendly summary, `## Features`/`## Bug fixes`, emojis) contradict the unchanged evals. Expected to score below the baseline and be demoted, which is the point of the beat. The score is whatever the evaluation produces.

`demo/push-skill.sh <skill> <version> [dir]` publishes any directory as a version, so more variants are a directory away. A variant at `demo/variants/<skill>/<version>/` is picked up automatically.

Check the datasets without any LLM call: `make check-skills`. SkillEvaluator's static checks: `skillevaluator tier1 skills/<name>` (its secret and security scanners, `gitleaks` and `skillspector`, are not installed here, so it reports the run as incomplete although every check that ran passes).

## What was tested (2026-10-30)

1. **Trials in Agent Sandbox on DOKS.** SkillEvaluator ran from a Kubernetes Job with in-cluster credentials. Each trial was an Agent Sandbox; BuildKit built the task images into DOCR; no sandboxes were left behind.
2. **Model matrix.** Same skill, three pinned models, three parallel Jobs:

   | Model | With skill | Without | Lift | Job time |
   | --- | --- | --- | --- | --- |
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
make up                       # or: cd infra && pulumi up

# Images, controller, skills
make docr-login build deploy
kubectl -n skill-ci create secret generic llm-keys --from-literal=anthropic="$ANTHROPIC_API_KEY"
make seed RETRIGGER=1
make status

# Tear down: deletes the cluster. Run it by hand.
cd infra && pulumi destroy
```
