# Sample skills and walkthrough

The repository ships three sample skills under `skills/` and a set of `make` targets that drive the whole loop: push a skill, watch it get evaluated, push a better and a worse version, change the model list. This page describes the samples and walks through the loop. It works the same on the Pulumi reference stack and on a cluster installed with [deploy/README.md](../deploy/README.md), because the scripts in `demo/` reach the cluster with `kubectl` only.

## The sample skills

The sample skills are examples written to exercise skill-ci, not production skills. Each has three eval cases in the agentskills.io format. With 3 attempts per arm that makes 9 trials per arm, a small sample, which is why the gate uses confidence intervals. The datasets were checked with SkillEvaluator's dataset loader and `skillevaluator tier1`.

The table shows the lift over no skill measured on release 0.7.x for Claude Code + Opus 5.5 / Codex + gpt-6.1-sol / OpenCode + Qwen 3.8 Flash. Only Claude Code is required; the other two never gate.

| Skill | What it encodes | Measured lift and outcome |
| --- | --- | --- |
| `acme-release-notes` | A made-up release note format (`ACME-RN/2`) | v1.0.0 +0.59 / +0.25 / +0.57, with-skill 0.95 / 0.72 / 0.96. No model can know the format without the skill |
| `k8s-resource-naming` | A team-specific naming and labelling convention for Kubernetes resources | v1.0.0 +0.57 / +0.20 / +0.54, `stable`. The conventions (`prd` instead of `prod`, `-cfg` and `-sec` suffixes, `acme.io/*` labels) are arbitrary, so a model cannot guess them |
| `python-docstrings` | Google-style docstrings | v1.0.0 +0.38 / +0.13 / +0.39, `stable`. The baseline is higher (0.46 to 0.58) because models half know the convention. Not Obsolete, because the no-skill scores are below 0.80. On an earlier list with Haiku required it gained only +0.03 there and stayed on `beta` |

Further versions live in `demo/variants/` (see [demo/variants/README.md](../demo/variants/README.md)):

- `acme-release-notes/v1.1.0` adds two rules (sort bullets by ticket ID, `- None [ACME-0]` for an empty section) and matching assertions. Measured: with-skill 0.98 / 0.72 / 0.95, lift +0.61 / +0.27 / +0.57, so it takes over `stable`.
- `acme-release-notes/v1.2.0` is a deliberate regression. Its rules (a `# Release` heading, friendly summary, `## Features`/`## Bug fixes`, emojis) contradict the unchanged evals. Measured on Claude Code: with-skill 0.69, still a positive lift (+0.33) over no skill, but -0.29 against stable v1.1.0 (95% CI [-0.31, -0.27]). The regression gate marks it `Regressed` and keeps the version at `beta`. The optional entries drop too (Codex 0.47, `Neutral` at +0.01; OpenCode 0.72, still `Helps` at +0.35) and gate nothing.
- `k8s-resource-naming/v1.1.0` is deliberately unsafe. Tier 1 rejects it, Tier 3 never runs, and the verdict is `Rejected`.

`demo/push-skill.sh <skill> <version> [dir]` publishes any directory as a version, so more variants are a directory away. A variant at `demo/variants/<skill>/<version>/` is picked up automatically.

## Walkthrough

`make help` lists the targets. The scripts print what they do in plain words. A SkillEval takes 11 to 13 minutes and three run at a time, so each step below takes a while to settle.

1. Seed the samples. `make seed` pushes every skill under `skills/` as `v1.0.0` through the toolbox pod and skips tags that exist; `RETRIGGER=1` re-fires the push webhook for existing tags. `make status` shows the SkillEvals and the tags.
2. Push a better version. `make demo-push` publishes `acme-release-notes:v1.1.0`. The controller evaluates it against every model on the list, and it takes over `stable`.
3. Push a regression. `make demo-push VERSION=v1.2.0` publishes a version whose rules contradict its own evals. The regression gate holds it at `beta`. A drop that is within noise is held back too, as `Inconclusive` rather than `Regressed`.
4. Change the model list. `make demo-model-swap` adds a model. The controller evaluates the current `beta` and `stable` digests against it.
5. Change the harness. `make demo-model-swap MODE=bump` moves Claude Code to a newer release. A new agent version is a new model list entry, so every `beta` and `stable` skill version runs again on it.
6. Look at the results. `make reports-ui` port-forwards the reporting site, `make dashboard` port-forwards Grafana (open the `skill-ci` dashboard there), and `make report SKILL=acme-release-notes VERSION=v1.1.0` pulls the SkillEvaluator reports attached to that version.
7. Start over. `make reset` deletes SkillEvals and Jobs, removes the `beta` and `stable` tags and the extra versions from Harbor, and restores the model list.

## Make targets

| Target | What it does |
| --- | --- |
| `make up` | `pulumi up` of the reference stack: cluster, Harbor, secrets, project and webhook, toolbox, model list, Prometheus and Grafana |
| `make deploy` | `pulumi up` with the stack's skill-ci release: the CRDs and the controller from the OCI chart `oci://ghcr.io/dirien/charts/skill-ci-controller`, with the GHCR images of the same release (`TAG=0.8.1` sets the stack config `skillCiVersion` first) |
| `make sandbox-image` | Builds the sandbox image locally as `skill-ci-sandbox:local` (needs Docker) |
| `make seed` | Pushes every skill under `skills/` as `v1.0.0` through the toolbox pod. Skips tags that exist. `RETRIGGER=1` re-fires the push webhook for existing tags |
| `make demo-push` | Pushes a new version of a skill (`SKILL=acme-release-notes VERSION=v1.1.0`) |
| `make demo-model-swap` | Changes the model list live: `MODE=add` adds an optional model, `swap` replaces the Claude model, `bump [TO=2.1.308]` moves Claude Code to another release (the harness changed), `require AGENT=codex` / `optional AGENT=codex` flips whether an agent gates promotion, `restore` resets |
| `make status` | `kubectl get skillevals -o wide` plus tags and digests per skill from the Harbor API |
| `make report` | Downloads the eval reports attached to a skill version in Harbor (`SKILL=acme-release-notes VERSION=v1.1.0 [MODEL=...] OUT=reports/`) |
| `make report-page` | Builds an offline page with every SkillEval and the SkillEvaluator reports (`OUT=reports/index.html`) |
| `make reports-ui` | Port-forwards the controller's reporting site to `localhost:8082` and prints the URL |
| `make dashboard` | Port-forwards Grafana to `localhost:3000` and prints the login hint |
| `make reset` | Deletes SkillEvals and Jobs, removes `beta`/`stable` tags and the extra versions from Harbor, restores the model list |
| `make check-skills` | Checks the datasets of the sample skills without any LLM call |
| `make draft-evals` | Drafts `evals/evals.json` for a skill, for you to review (`SKILL=...`, `DIR=...`, `FORCE=1`) |
| `make down` | Refuses on purpose. Destroying the stack deletes the cluster, run `pulumi destroy` by hand |
| `make lint` | shellcheck, yamllint, markdownlint, JSON checks and the dataset check of the skills |
| `make chart-lint` | `helm lint --strict`, kubeconform, yamllint and a packaging check of the chart |

On a cluster installed with `deploy/install.sh`, the Pulumi targets (`make up`, `make preview`, `make down`) and `make deploy` do not apply.

## Your own skills

A skill is a directory named exactly like the skill, with a `SKILL.md` and its test cases. Push it with `demo/push-skill.sh <skill> <version> [dir]` or with `oras push` as described in [architecture.md](architecture.md#from-push-to-tag). Declare its network needs in `SKILL.md`, see [network-and-sandboxes.md](network-and-sandboxes.md#network-modes).

Check the datasets without any LLM call: `make check-skills` (a case whose `expected_skill` is null is a negative, off-skill case).

A skill needs test cases before the live evaluation can run: without `evals/evals.json` it gets Tier 1 and 2 only, and the run fails with "no eval dataset". To start one, `make draft-evals SKILL=<name>` (or `DIR=<skill dir>`) runs SkillEvaluator's `create-eval-dataset --full` in a short-lived pod from the runner image with the Anthropic key of `llm-keys`: one LLM request, about 20 seconds, four cases (explicit, implicit, contextual, negative). It never overwrites an existing file unless `FORCE=1`, fails instead of writing SkillEvaluator's generic template when the LLM call did not happen, and prints a review checklist.

Review the draft. The generator reads `SKILL.md`, so its assertions repeat the skill's own wording and cannot catch a rule the skill forgot. It also only checks the reply, so add assertions on files the agent has to write. skill-ci never drafts or regenerates test cases inside the gate.

A skill with `requirements.txt`, `apt-packages.txt` or a custom `evals/environment/Dockerfile` needs steps the sandbox image does not have and fails with a message that names the step, see [architecture.md](architecture.md#the-sandbox-image).
