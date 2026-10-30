# Skills Are Packages Now: test stack

Proof that the skill CI loop from the talk runs on generic Kubernetes (DOKS), built from open source parts:

| Part | Role | Version |
| --- | --- | --- |
| [goharbor](https://goharbor.io) | Stores skills as OCI artifacts, fires `PUSH_ARTIFACT` webhooks | 2.15.4 (chart 1.19.4) |
| [ORAS](https://oras.land) | Pushes, pulls and tags skills | 1.3.0 |
| [NVIDIA SkillEvaluator](https://github.com/NVIDIA/SkillEvaluator) | Live with/without-skill runs, lift scoring | 0.6.0 + [patch](skillevaluator-patch/) |
| [Harbor framework](https://harborframework.com) | Runs agent trials | 0.24.0 (pinned by SkillEvaluator) |
| [Agent Sandbox](https://github.com/kubernetes-sigs/agent-sandbox) | One `Sandbox` per trial | v1.0.9 |
| DOKS | The cluster. The images come from GHCR, no registry of its own | Kubernetes 1.36.5 |
| [kube-prometheus-stack](https://github.com/prometheus-community/helm-charts) | Prometheus and Grafana for the controller metrics | chart 92.10.1 |
| skill-ci controller | Watches pushes and the model list, runs SkillEvals as Jobs, moves `beta`/`stable` | 0.6.0 (Helm chart `charts/skill-ci-controller`) |
| skill-ci runner | One Job per SkillEval: ORAS pull, SkillEvaluator Tier 1, 2 and 3, per-trial scores, report upload | 0.6.0 |
| skill-ci sandbox image | The one image every trial sandbox starts from: Python, the verifier packages and the Claude Code, Codex and OpenCode CLIs at their pins. Built once per release, never in the cluster | 0.6.0 |

The interfaces between the parts are in [CONTRACT.md](CONTRACT.md). The stage script for the talk is [DEMO.md](DEMO.md).

Slides: the talk deck is in [slides/](slides/) (Slidev; `cd slides && npm ci && npm run dev`, diagrams in `slides/diagrams/`).

## Layout

```text
infra/                 Pulumi YAML: DOKS, Agent Sandbox, goharbor, secrets, Harbor setup Job, toolbox, model list, Prometheus + Grafana
infra/dashboards/      Grafana dashboard JSON (provisioned through a ConfigMap)
skills/                Sample skills with evals (demo material, see below)
demo/                  Shell scripts behind the Makefile targets, skill version variants, initial model list
controller/            The skill-ci controller (Go)
charts/                Helm chart of the controller (what `make deploy` installs)
config/                Controller manifests (CRD source, RBAC, Deployment, Service), plain `kubectl apply -k` alternative
reports/               Eval reports downloaded by `make report`
harbor-agent-sandbox/  Harbor environment that runs each trial as an agents.x-k8s.io Sandbox from the generic sandbox image
sandbox/               Dockerfile of that image (Python, verifier packages, agent CLIs) and the script that installs the CLIs
skillevaluator-patch/  Eleven-hunk patch: that environment and its sandbox image and sizing, a Tier 2 catalog for validate, the agent CLI version, OpenCode behind an OpenAI-compatible gateway
runner/                In-cluster runner image and the SkillEval Job (ORAS init container + SkillEvaluator)
.github/               CI and release workflows, Dependabot (see Releases)
.goreleaser.yaml       Controller binaries and image of a release
```

## Helm chart

`charts/skill-ci-controller/` packages the controller (CRD, RBAC, runner ServiceAccount, Deployment, Service, optional ServiceMonitor) as a Helm 4 chart. `make deploy` installs it (and applies the CRD first, because Helm never upgrades `crds/`); `kubectl apply -k config/` still works as a plain-manifest alternative. `make chart-crds` copies `config/crd/` into the chart, `make chart-lint` runs `helm lint --strict`, kubeconform, yamllint and a packaging check. See [the chart README](charts/skill-ci-controller/README.md) for values, adopting an existing install with `--take-ownership` and pushing the chart to an OCI registry.

## Releases

Pushing a tag `vX.Y.Z` runs `.github/workflows/release.yaml` and publishes everything from the tag with the workflow's `GITHUB_TOKEN` (no other secret). Tags with a suffix (`v0.7.0-rc.1`) are pre-releases and do not move `latest`.

| What | Where | Built by |
| ---- | ----- | -------- |
| Controller binaries (linux, darwin; amd64, arm64), checksums, SBOMs, release notes | GitHub release | GoReleaser (`.goreleaser.yaml`) |
| Controller image, linux/amd64 and linux/arm64 | `ghcr.io/dirien/skill-ci-controller:X.Y.Z` | GoReleaser, `controller/Dockerfile.goreleaser` |
| Runner image, linux/amd64 | `ghcr.io/dirien/skill-ci-runner:X.Y.Z` | `docker/build-push-action`, `runner/Dockerfile` |
| Sandbox image, linux/amd64 | `ghcr.io/dirien/skill-ci-sandbox:X.Y.Z` | `docker/build-push-action`, `sandbox/Dockerfile` |
| Helm chart, version and appVersion `X.Y.Z` | `oci://ghcr.io/dirien/charts/skill-ci-controller` | `helm package` + `helm push` |

The checksums file and the three images are signed with Cosign (keyless, the workflow identity); the runner and sandbox images carry an SBOM and provenance attestation.

The chart in the repository and the published chart are the same: the default images are `ghcr.io/dirien/skill-ci-controller`, `skill-ci-runner` and `skill-ci-sandbox`, tagged with the chart's `appVersion` (which `helm package --app-version X.Y.Z` sets from the release tag), and `imagePullSecrets` is empty.

```bash
helm install skill-ci oci://ghcr.io/dirien/charts/skill-ci-controller --version X.Y.Z \
  --namespace skill-ci --create-namespace
```

The GHCR packages of a private repository are private. Make the four packages (three images and the chart) public in the package settings, which is how they are meant to be used, or give the cluster pull credentials: a `docker-registry` Secret in the namespace and `--set 'imagePullSecrets[0].name=ghcr'`, which the chart uses for the controller and passes to the controller for the runner Jobs and the sandboxes. Helm never upgrades `crds/`, so apply the CRD of the new version before an upgrade: `helm show crds oci://ghcr.io/dirien/charts/skill-ci-controller --version X.Y.Z | kubectl apply --server-side -f -`.

The demo flow: tag a release, then `make deploy` installs the working-tree chart with the images of the newest `vX.Y.Z` tag (`make deploy TAG=0.7.0` to pick one). Nothing is built in the cluster. `make release-snapshot` dry-runs the GoReleaser build locally (needs `goreleaser` and Docker), `make sandbox-image` builds the sandbox image locally.

### CI

| Workflow | Trigger | Checks |
| -------- | ------- | ------ |
| `ci.yaml` | push and PR on main | `go vet`, `go test -race`, `go build`; pytest; Docker build of the controller, runner and sandbox images (no push; the sandbox build installs and version-checks the three agent CLIs); `goreleaser check` and a snapshot release |
| `lint.yaml` | push and PR on main | golangci-lint; ruff check and format; `make lint` (shellcheck, yamllint, markdownlint, JSON, sample skills); actionlint |
| `chart.yaml` | push and PR on main | generated code and CRDs up to date; `make chart-lint`; the chart packages and renders with the GHCR images of its appVersion and no pull secret |
| `release.yaml` | tag `v*` | tests, then GoReleaser, runner image, sandbox image and chart as above |

Dependabot (`.github/dependabot.yaml`) keeps the actions, Go modules, Python pins and Dockerfile base images current.

## Run the demo

Everything goes through `make` (`make help` lists the targets). Scripts print what they do in plain words.

| Target | What it does |
| --- | --- |
| `make up` | `pulumi up`: cluster, Harbor, secrets, project and webhook, toolbox, model list, Prometheus and Grafana |
| `make deploy` | Applies the CRD and installs the controller with its Helm chart (`helm upgrade --install --take-ownership`) and the GHCR images of the newest release tag (`TAG=0.7.0`, or `CONTROLLER_TAG`, `RUNNER_TAG`, `SANDBOX_TAG` one by one; `GHCR_PULL_SECRET=ghcr` for private packages) |
| `make sandbox-image` | Builds the sandbox image locally as `skill-ci-sandbox:local` (needs Docker) |
| `make seed` | Pushes every skill under `skills/` as `v1.0.0` through the toolbox pod. Skips tags that exist. `RETRIGGER=1` re-fires the push webhook for existing tags |
| `make demo-push` | Pushes a new version of a skill (`SKILL=acme-release-notes VERSION=v1.1.0`) |
| `make demo-model-swap` | Changes the model list live: `MODE=add` adds an optional model, `swap` replaces the Claude model, `bump [TO=2.1.308]` moves Claude Code to another release (the harness changed), `require AGENT=codex` / `optional AGENT=codex` flips whether an agent gates promotion, `restore` resets |
| `make status` | `kubectl get skillevals -o wide` plus tags and digests per skill from the Harbor API |
| `make report` | Downloads the eval reports attached to a skill version in Harbor (`SKILL=acme-release-notes VERSION=v1.1.0 [MODEL=...] OUT=reports/`) |
| `make reports-ui` | Port-forwards the controller's reporting site to `localhost:8082` and prints the URL |
| `make dashboard` | Port-forwards Grafana to `localhost:3000` and prints the login hint |
| `make reset` | Deletes SkillEvals and Jobs, removes `beta`/`stable` tags and demo versions from Harbor, restores the model list |
| `make down` | Refuses on purpose. Destroying the stack deletes the cluster, run `pulumi destroy` by hand |
| `make lint` | shellcheck, yamllint, markdownlint, JSON checks and the dataset check of the skills |
| `make chart-lint` | `helm lint --strict`, kubeconform, yamllint and a packaging check of the chart |

Suggested order on a fresh checkout, once a release is tagged: `make up`, `make deploy`, `make seed RETRIGGER=1`, `make status`. The retrigger matters when the skills were pushed before the controller existed, because Harbor retries a failed webhook delivery only a few times.

Demo beats after that (a SkillEval takes 11 to 13 minutes and three run at a time, so [DEMO.md](DEMO.md) pre-bakes the results and uses at most one live trigger):

1. **Push a better version.** `make demo-push` publishes `acme-release-notes:v1.1.0`. The controller evaluates it against every model on the list.
2. **Push a regression.** `make demo-push VERSION=v1.2.0` publishes a version whose rules contradict its own evals. The regression gate holds it at `beta` (see below). A drop that is within noise is held back too, as `Inconclusive` rather than `Regressed`.
3. **Change the model list.** `make demo-model-swap` adds a model. The controller evaluates the current `beta`/`stable` digests against it.
4. **Change the harness.** `make demo-model-swap MODE=bump` moves Claude Code to a newer release. A new agent version is a new model list entry, so every `beta` and `stable` skill version runs again on it.
5. **Show the metrics and reports.** `make dashboard`, then open the `skill-ci` dashboard. `make report SKILL=acme-release-notes VERSION=v1.1.0` pulls the SkillEvaluator reports attached to that version.

## Agents and their versions

The harness is part of the measurement, so skill-ci treats the agent and its release like the model. An entry of the model list is agent + `agentVersion` + model (`demo/models.yaml` has one for each supported agent):

| Agent | Pinned release | Model | Needs in `llm-keys` |
| --- | --- | --- | --- |
| `claude-code` | 2.1.292 (SkillEvaluator's pin) | `claude-opus-5-5` | `anthropic` |
| `codex` | 0.161.0 (SkillEvaluator's pin) | `openai/<id>` | `openai`, optionally `openai-base-url` |
| `opencode` | 1.19.1 (skill-ci's pin: upstream pins none, 1.19.1 was published on 2026-10-15) | `ollama-cloud/gpt-oss:120b`, `anthropic/<model>`, ... | `ollama` (or `openrouter`, `nvidia`, `anthropic`) |

- **A new agent version is a new entry**, so bumping a version re-evaluates every `beta` and `stable` digest on it. Results stay per entry: the regression gate compares a candidate with the stable version on the same `agent@version/model`, never across harnesses.
- **`required` per entry decides what gates promotion.** The demo list gates on Claude Code and runs Codex and OpenCode as optional, so they show up everywhere without holding a skill back. `make demo-model-swap MODE=require AGENT=codex` makes Codex gate (the runs that already exist are re-judged at once); `MODE=optional AGENT=codex` undoes it.
- **The judge is always Anthropic**, whatever agent runs. Codex gets `OPENAI_API_KEY` and `OPENAI_BASE_URL`; OpenCode models other than `anthropic/...` run through the provider's OpenAI-compatible endpoint (patch hunks 6 and 8 to 11). The routes and the checks are in [CONTRACT.md](CONTRACT.md) ("Agent versions", "LLM keys").
- Upgrading from 0.5: runs created before agent versions have no `agent-version` label and count as the entries without `agentVersion`. Pinned entries are new entries, so applying the pinned list re-evaluates the `beta` and `stable` digests; keep an unpinned entry in the list, or delete the old SkillEvals (`make reset`), to avoid mixing.

## Reporting site

The controller serves a read-only reporting site on port 8082 (`--reports-addr`, empty disables it; Helm `reports.enabled`). `make reports-ui` port-forwards it to <http://localhost:8082>.

- `/` lists every run (SkillEval) newest first, with release channels per skill and filters (`?skill=`, `?agent=claude-code@2.1.292`, `?model=`, `?verdict=`, `?phase=`).
- `/skills/<skill>` shows versions against model list entries (one column per `agent@version` and model) with the lift (and its confidence interval and n), the channel tags and the policy decision per version in plain words.
- `/runs/<name>` shows one run: scores with the lift interval and n, conditions and, when a report is stored, the six SkillEvaluator dimensions with and without the skill. The full SkillEvaluator report and `result.json` open from there.

Reports are read from Harbor as OCI referrers of the evaluated skill digest and cached in memory. Runs from runners before 0.3.0 stored none and show "not stored". The pages need no internet, apart from the full SkillEvaluator report, which loads Chart.js from a CDN. `make report-page` builds the offline equivalent.

## How the gate decides

A fixed number is a bad gate for a noisy measurement. The same skill version scored 0.95 on one run and 0.87 on the next, and a rule of "more than 0.05 below stable is a regression" held a good version back. Evidence-based evaluation does not do that: SWE-Skills-Bench, for one, found that 92 of 98 skills had no statistically significant effect. So the gate asks for evidence.

Each run has 3 eval cases x 3 attempts per arm, so up to 9 trial scores with the skill and 9 without. The runner sends them (`samples` in the termination message), the controller computes a confidence interval (Welch's t, 95% by default) and decides:

| Question | Rule |
| --- | --- |
| Does the skill help? | Lift is at least +0.10 **and** the lift interval is entirely above 0, on every required model. Otherwise the version is not promoted. |
| Did the new version get worse than stable? | With-skill score is more than 0.05 below stable on a required model **and** the interval of the difference is entirely below 0: `Regressed`. |
| Is the drop only maybe real? | More than 0.05 below, but the interval includes 0: `Inconclusive`. Not promoted, stable stays, `beta` is kept, and the numbers are in the condition. |
| Do older runs still work? | A run without trial scores (runner before 0.5.1) uses the fixed thresholds above and says so in its condition. |

`--confidence` (Helm `controller.thresholds.confidence`, default 0.95) sets the level: a higher value asks for more evidence both to promote and to call a regression. Worked example, stable `v1.0.0` at 0.95 on a required model:

- `v1.2.0` scores 0.70 with tight trials: difference -0.25, 95% CI [-0.26, -0.24], `Regressed`.
- `v1.2.0` scores 0.87 with spread-out trials: difference -0.08, 95% CI [-0.18, +0.03], `Inconclusive`. The drop is bigger than the tolerance, but noise could explain it.

The formulas and the exact rules are in [CONTRACT.md](CONTRACT.md) ("Statistics" and "Policy").

## Policy in short

Per SkillEval, the lift over the no-skill run gives the verdict (`Helps` from +0.05, `Hurts` at -0.10 or lower, each only when the lift interval clears 0; `Obsolete` when a neutral skill has a no-skill score of 0.80 or more). Per skill digest, once every required model has a result:

- The newest evaluated digest gets `beta`.
- A digest with lift of at least +0.10 on every required model (interval above 0) gets `stable`, unless it fails the **regression gate**: its with-skill score on a required model is more than 0.05 below the current stable digest's score and the interval of that difference is below 0. Then it keeps `beta`, its SkillEvals get a `Regressed` condition (with the models, scores and interval in the message) and the verdict `Regressed` on the models that dropped. When the interval includes 0 the verdict is `Inconclusive` instead, with the same consequences for the tags. Optional models never gate.
- An older version does not take `stable` from a newer one unless the newer one scores worse than it.
- Verdicts depend on other digests, so when a SkillEval succeeds or the policy changes a tag, the controller re-runs the policy for the other finished SkillEvals of the same skill.

The details and the flags are in [CONTRACT.md](CONTRACT.md).

## Infrastructure notes

- **Harbor** has `externalURL: http://harbor.harbor.svc.cluster.local`, so pods can reach its token service. From outside the cluster, reach it only through in-cluster tooling: the toolbox pod or `kubectl -n harbor port-forward svc/harbor`.
- **Setup Job.** `harbor-setup-<id>` waits for Harbor, creates the private project `skills` if it is missing, and creates or updates the webhook policy `skill-ci` (`PUSH_ARTIFACT`, payload `Default`, `Authorization: Bearer <harbor-webhook token>`) pointing at the controller Service. It also removes policies that still point at the old debug receiver. The script is `infra/harbor-setup.sh`. The Job name carries a random id that changes when the script, the image, the admin password or the token change, so every relevant `pulumi up` runs a fresh, idempotent Job.
- **Secrets** in `skill-ci`: `harbor-credentials` (user `admin` and the generated admin password), `harbor-pull` (Docker config for `harbor.harbor.svc.cluster.local`, same credentials), `harbor-webhook` (random token). A project-scoped Harbor robot account is the production choice. Admin keeps the demo reproducible from one `pulumi up`. `llm-keys` is not managed by Pulumi. Create it by hand and never commit key values: `kubectl -n skill-ci create secret generic llm-keys --from-literal=anthropic="$ANTHROPIC_API_KEY"`. Only `anthropic` is required (it is the judge). Add the keys of the agents you evaluate: `openai` (Codex, Tier 2 embeddings), `ollama`, `openrouter`, `nvidia` (OpenCode models behind those providers), `openai-base-url` (a Codex endpoint other than api.openai.com); see [the agent section](#agents-and-their-versions).
- **Toolbox.** Deployment `toolbox` has two containers: `oras` (pinned image, the `harbor-pull` secret at `/auth/config.json`) and `harbor-api` (curl with the admin password in its env). The scripts copy a skill directory in with `tar` over `kubectl exec` and run `oras push --plain-http --registry-config /auth/config.json`.
- **Sandbox sizing.** Each trial runs in one Agent Sandbox. The runner image defaults are `SANDBOX_CPUS=1`, `SANDBOX_MEMORY_MB=2048` (per sandbox) and `TRIAL_CONCURRENCY=2` (trials at once per Job); `SANDBOX_NAMESPACE` and `SANDBOX_IMAGE` (the sandbox image of the runner's release) are set there too; the controller overrides `SANDBOX_IMAGE` from `--sandbox-image` (Helm `controller.sandboxImage`). Override them as env vars on the runner container (the manual Job in `runner/job.yaml` shows where). The controller caps the Jobs: Helm value `controller.maxConcurrentJobs` (`--max-concurrent-jobs`, deployed with 3) and `controller.trialBudget` (`--trial-budget`, 200). Three Jobs at two trials each is up to 6 sandboxes of 1 vCPU and 2 GiB at a time.
- **Model list.** ConfigMap `skill-ci-models` (an entry is agent + agent version + model, see [the agent section](#agents-and-their-versions)) is seeded from `demo/models.yaml` and has `ignoreChanges: [data]` in Pulumi, so demo edits survive later `pulumi up` runs. `make reset` restores it from the same file.
- **Monitoring.** `kube-prometheus-stack` in namespace `monitoring`: Prometheus (24h retention) and Grafana, with Alertmanager, node exporter, kube-state-metrics, default rules and the control-plane scrape jobs switched off. A `ServiceMonitor` in `skill-ci` scrapes Service `skill-ci-controller` (port `metrics`, label `app.kubernetes.io/name=skill-ci-controller`). The dashboard `infra/dashboards/skill-ci.json` is a ConfigMap with the `grafana_dashboard` label. It shows the lift matrix per skill version, `agent@version` and model (`skillci_lift`; every per-run series has `agent` and `agent_version` labels), the lift with its confidence interval (`skillci_lift_ci_lower`, `skillci_lift_ci_upper`), scores with and without the skill, `skillci_stable`, `skillci_obsolete`, the Inconclusive and Regressed counts and `skillci_skillevals_total`. The Grafana admin password is generated and exported as the secret stack output `grafanaAdminPassword`.
- **Sandbox image.** `sandbox/Dockerfile` is one image for every trial, built once per release by the release workflow: no registry to pay for, no builder in the cluster, no build before the first trial. It is about 3 GB on disk (1 GB compressed), so the first trial on a new autoscaled node waits for the pull (the sandbox waits up to 10 minutes to become ready) and later ones start from the node's cache. How it stands in for the per-task Dockerfiles, and what happens to a skill whose Dockerfile needs more, is in [CONTRACT.md](CONTRACT.md) ("Sandbox image").
- **Cost.** There is no registry or builder to run: the images are public GHCR packages. Prometheus and Grafana run on the existing two nodes (requests of about 0.2 vCPU and 0.6 GiB in total). Live evals cost LLM tokens, see the cost guard flags in `CONTRACT.md`.

## Sample skills

The skills under `skills/` are demo material written for this talk, not production skills. Each has three eval cases in the agentskills.io format (3 attempts per arm when deployed, so 9 trials per arm: a small sample, which is why the gate uses confidence intervals). The datasets were checked with SkillEvaluator's dataset loader and `skillevaluator tier1`. Live results on the cluster (2026-10-30, lift over no skill, models haiku / sonnet / opus; opus is optional and never gates):

| Skill | What it encodes | Measured lift and outcome |
| --- | --- | --- |
| `acme-release-notes` | A made-up release note format (`ACME-RN/2`) | v1.0.0 +0.60 / +0.63 / +0.59, with-skill 0.95 on all three. No model can know the format without the skill |
| `k8s-resource-naming` | A team-specific naming and labelling convention for Kubernetes resources | v1.0.0 +0.57 / +0.62 / +0.57, `stable`. The conventions (`prd` instead of `prod`, `-cfg` and `-sec` suffixes, `acme.io/*` labels) are arbitrary, so a model cannot guess them |
| `python-docstrings` | Google-style docstrings | v1.0.0 +0.03 (haiku, Neutral) / +0.43 / +0.41. Stays `beta`: promotion needs +0.10 on every required model. Not Obsolete, because the no-skill scores (0.52 to 0.59) are below 0.80. Haiku scores almost the same with and without the skill; sonnet and opus gain about +0.4 |

Versions of `acme-release-notes` for the push beat live in `demo/variants/acme-release-notes/`:

- `v1.1.0` adds two rules (sort bullets by ticket ID, `- None [ACME-0]` for an empty section) and matching assertions. Measured: with-skill 0.95 on all three models, lift +0.58 / +0.64 / +0.58, so it takes over `stable`.
- `v1.2.0` is a deliberate regression. Its rules (a `# Release` heading, friendly summary, `## Features`/`## Bug fixes`, emojis) contradict the unchanged evals. Measured: with-skill 0.69 / 0.70 / 0.71, still a positive lift (+0.33 / +0.39 / +0.34) over no skill, but 0.24 to 0.26 below stable. The regression gate marks haiku and sonnet `Regressed` and keeps the version at `beta`; opus is optional and keeps `Helps`.

`demo/push-skill.sh <skill> <version> [dir]` publishes any directory as a version, so more variants are a directory away. A variant at `demo/variants/<skill>/<version>/` is picked up automatically.

Check the datasets without any LLM call: `make check-skills`.

### Tiers and scanners in the runner

The runner image evaluates every skill version through all three SkillEvaluator tiers (see `CONTRACT.md`, "All tiers and publication"). The gate is `skillevaluator validate <skill> --tiers 1,2 --no-llm --profile external --continue-on-failure --catalog <catalog.json>` (Tier 1 static and security checks, Tier 2 deduplication; the catalog holds the other skills of the Harbor project at `stable`, else at their newest `vX.Y.Z`); a failure there skips Tier 3. Otherwise one more `validate --tiers 1,2,3` run adds the live Tier 3 evaluation, so SkillEvaluator's own report (`report.html`, `BENCHMARK.md`) carries a complete publication assessment, which the runner reads from `publication.status`. Two patch hunks make that possible: `validate --catalog`, and the sandbox size (`SANDBOX_CPUS`, `SANDBOX_MEMORY_MB`) reaching the sandbox environment as `HARBOR_SANDBOX_CPUS` and `HARBOR_SANDBOX_MEMORY_MB`, because `validate` has no `--override-cpus`. Tier 1 is only trustworthy with its scanners, so the image pins them: Gitleaks 8.30.1 (binary, sha256-checked), SkillSpector v2.12.0 and Semgrep 1.183.0 (each in its own virtualenv, they pin conflicting click versions), Bandit 1.9.4 and pip-audit 2.10.1 (SkillEvaluator's `security` extra). Without them SkillEvaluator marks Tier 1 `incomplete`. Tier 2 needs an embeddings provider, and Anthropic has none: put an `openai` key into secret `llm-keys` or Tier 2 reports `incomplete`.

The profile is SkillEvaluator's built-in `external`, the only one it ships. For skills published to an internal platform it asks for nothing the demo skills lack (a `Name <email>` author, a permissive license, no blocked licenses) and keeps license findings blocking. Override with `TIER1_PROFILE`.

`demo/variants/k8s-resource-naming/v1.1.0` is a deliberately unsafe variant that Tier 1 rejects, see `demo/variants/README.md`. To run the checks locally, install the same scanners and run `skillevaluator validate skills/<name> --tiers 1,2`; the skill directory must be named like the skill.

## What was tested (2026-10-30)

1. **Trials in Agent Sandbox on DOKS.** SkillEvaluator ran from a Kubernetes Job with in-cluster credentials. Each trial was an Agent Sandbox; no sandboxes were left behind. (That run built a task image per trial in the cluster. The sandbox image replaced the builder afterwards, see "Findings".)
2. **Model matrix.** Same skill, three pinned models, three parallel Jobs:

   | Model | With skill | Without | Lift | Job time |
   | --- | --- | --- | --- | --- |
   | claude-haiku-5-5 | 0.95 | 0.36 | +0.59 | 4m07s |
   | claude-sonnet-5-5 | 0.95 | 0.32 | +0.64 | 6m52s |
   | claude-opus-5-5 | 0.95 | 0.36 | +0.58 | 7m59s |

   The test skill (`acme-release-notes`) teaches a made-up format, so no model knows it without the skill. It proved the pipeline, not a regression. The regression gate is exercised by `acme-release-notes` v1.2.0, which was written to fail (see above).
3. **Registry trigger.** `oras push` fires `PUSH_ARTIFACT`. `oras tag` (promotion) fires `PUSH_ARTIFACT` again with the same digest. Deleting a tag through the Harbor API (demotion) fires nothing.

## Findings that shape the build

- **Harbor's generic Kubernetes backend (`ack`) is thin.** In DinD mode it ships only the Dockerfile to the build Job, so every `COPY` fails. Its context archive drops empty directories, which breaks the no-skill baseline. It has no Harbor network or resource policy support. `harbor-agent-sandbox` fixes the build context and swaps the privileged pod for an unprivileged `Sandbox`.
- **SkillEvaluator hard-codes Harbor environments** and filters the environment variables it passes to Harbor. The patch adds `SKILLEVALUATOR_ACK_ENV_IMPORT_PATH` and lets `HARBOR_SANDBOX_IMAGE` (and the size variables) through. Both changes are upstream PR candidates.
- **Harbor's task image names say nothing about the skill version.** SkillEvaluator names the task `skillevaluator-<eval case>-with-skill`, Harbor's `ack` backend turns that into `<registry>/skillevaluator-<eval case>-with-skill:latest`, and it builds only when an existence check says the image is missing. Two versions of one skill (or two skills with an eval case of the same name) share the image name, and the `COPY skills/` that carries the skill is baked into it. On the private registry the demo cluster used, the check always answered "missing" (an unauthenticated `HEAD` gets 401 and Harbor reads that as absent, and the runner image has neither `docker` nor `crane`), so every trial rebuilt and pushed `:latest`, which hid the problem and cost a build per trial; on a registry that answers the check, a later version is evaluated with an earlier version's skill. The sandbox image removes the build and with it the question: the skill is uploaded into the sandbox of each trial.
- **The generated Dockerfiles are the same list of steps for every plain skill.** For a plain skill (no `requirements.txt` or `apt-packages.txt`, no custom `evals/environment/Dockerfile`), SkillEvaluator 0.6.0 writes byte-identical Dockerfiles with and without the skill, for every skill and eval case (the skill is in the build context, not in the Dockerfile; the three demo skills are plain). That is what makes one generic image possible; a skill with `requirements.txt` or a custom environment Dockerfile is not covered and fails with an error that names the step.
- **Harbor skips the agent install for Claude Code and Codex, but not for OpenCode.** The first two compare `--version` with the requested release; OpenCode reinstalls nvm, Node and its npm package in every sandbox. `harbor_agent_sandbox.preinstalled` gives it the same check.
- **SkillEvaluator rejects symlinked eval directories**, so skills can't be mounted from a ConfigMap. Pull them with ORAS into an `emptyDir` instead, which is the design anyway.
- **The controller must deduplicate by digest, not tag**, or every promotion triggers a new eval.
- **A lift above zero is not enough to promote.** v1.2.0 of the sample skill still beats no skill by +0.33 or more. Only the comparison with the stable version catches that, hence the regression gate.
- **Verdicts depend on the other versions.** Results arrive in any order (a SkillEval takes 11 to 13 minutes and they run three at a time), so the stable version can finish after the candidate. The controller re-runs the policy for the sibling SkillEvals of a skill when one succeeds or a tag moves, and an older version never takes `stable` from a newer one that is not worse. Without the second rule two equally good versions swap the tag on every reconcile.
- **Tag changes and webhooks.** Tag changes through the Harbor API do not fire webhooks; `oras tag` does. The controller uses the API.

## Reproduce

```bash
# Infra (local state in ~/.pulumi-local; the sandbox proxy breaks Pulumi Cloud update tokens)
. ~/.pulumi-local/skill-ci-env.sh
make up                       # or: cd infra && pulumi up

# Images, controller, skills
make deploy                   # the images of the newest vX.Y.Z tag, or TAG=0.7.0
kubectl -n skill-ci create secret generic llm-keys --from-literal=anthropic="$ANTHROPIC_API_KEY"
make seed RETRIGGER=1
make status

# Tear down: deletes the cluster. Run it by hand.
cd infra && pulumi destroy
```
