# Sample skills and walkthrough

The repository ships sample skills under `skills/` and a set of `make` targets that drive the whole loop: push a skill, watch it get evaluated, push a better and a worse version, change the model list. This page describes the samples and walks through the loop. It works the same on the Pulumi reference stack and on a cluster installed with [deploy/README.md](../deploy/README.md), because the scripts in `demo/` reach the cluster with `kubectl` only. The stage runbook of the talk is [DEMO.md](../DEMO.md).

## The sample skills

The sample skills are examples written to exercise skill-ci, not production skills. Each has three eval cases in the agentskills.io format. With 3 attempts per arm that makes 9 trials per arm, a small sample, which is why the gate uses confidence intervals.

Two of them are the demo skills, both SRE and platform work. `make seed` pushes only these two (`SEED_ALL=1` pushes every skill). The table shows the lift over no skill measured on 2026-11-03, release 0.12.0 (scored without Discoverability), for Claude Code + Opus 5.5 / Codex + gpt-6.1-sol / OpenCode + Qwen 3.8 Flash. Only Claude Code is required; the other two never gate.

| Skill | What it encodes | Measured lift and outcome |
| --- | --- | --- |
| `k8s-incident-triage` | How the ACME on-call team triages a failing pod: an ACME cause code, the one input line that proves it, and the read-only next command for that cause, written to `TRIAGE.md`. Network mode `none` | v1.0.0 +0.43 / +0.34 / +0.45, with-skill 0.91 / 0.80 / 0.90. The cause codes and runbook commands are made up, so no model can produce them without the skill |
| `k8s-resource-naming` | A team-specific naming and labelling convention for Kubernetes resources | v1.0.0 +0.45 / +0.25 / +0.45, `stable`. The conventions (`prd` instead of `prod`, `-cfg` and `-sec` suffixes, `acme.io/*` labels) are arbitrary, so a model cannot guess them |

`acme-release-notes` (a made-up release note format) and `python-docstrings` (Google-style docstrings) are test skills: the controller, runner and UI tests and the fixtures use them, and the demo does not push them.

Further versions live in `demo/variants/` (see [demo/variants/README.md](../demo/variants/README.md)):

- `k8s-incident-triage/v1.1.0` adds two on-call rules (the source of the evidence line, an owner line) and matching assertions. Measured: with-skill 0.87 / 0.80 / 0.89, lift +0.43 / +0.39 / +0.47, so it takes over `stable`.
- `k8s-incident-triage/v1.2.0` is a deliberate regression. Its rules (write `INCIDENT.md`, no cause codes, a list of every idea) contradict the unchanged v1.1.0 evals. Measured on Claude Code: with-skill 0.59, still a lift (+0.16) over no skill above the +0.10 promotion threshold, but -0.27 against stable v1.1.0 (95% CI [-0.35, -0.19]). The regression gate marks it `Regressed` and keeps the version at `beta`. The optional entries drop too (Codex 0.52, `Neutral`; OpenCode 0.61, still `Helps` over no skill) and gate nothing. On Claude Code 2.1.300 the verdict is the same: 0.61 against 0.88, -0.27.
- `k8s-resource-naming/v1.1.0` is deliberately unsafe. Tier 1 rejects it, Tier 3 never runs, and the verdict is `Rejected`.
- `acme-release-notes/v1.1.0` and `v1.2.0` are the same pair for the test skill.

`demo/push-skill.sh <skill> <version> [dir]` publishes any directory as a version, so more variants are a directory away. A variant at `demo/variants/<skill>/<version>/` is picked up automatically.

## Walkthrough

`make help` lists the targets. The scripts print what they do in plain words. A SkillEval takes 11 to 13 minutes and three run at a time, so each step below takes a while to settle.

1. Seed the samples. `make seed` pushes the two demo skills as `v1.0.0` through the toolbox pod (`SEED_ALL=1` every skill under `skills/`) and skips tags that exist; `RETRIGGER=1` re-fires the push webhook for existing tags. `make status` shows the SkillEvals and the tags.
2. Push a better version. `make demo-push` publishes `k8s-incident-triage:v1.1.0`. The controller evaluates it against every model on the list, and it takes over `stable`.
3. Push a regression. `make demo-push VERSION=v1.2.0` publishes a version whose rules contradict its own evals. The regression gate holds it at `beta`. A drop that is within noise is held back too, as `Inconclusive` rather than `Regressed`.
4. Change the model list. `make demo-model-swap` adds a model. The controller evaluates the current `beta` and `stable` digests against it.
5. Change the harness. `make demo-model-swap MODE=bump` moves Claude Code to a newer release. A new agent version is a new model list entry, so every `beta` and `stable` skill version runs again on it.
6. Look at the results. `make reports-ui` port-forwards the reporting site, `make dashboard` port-forwards Grafana (open the `skill-ci` dashboard there), and `make report SKILL=k8s-incident-triage VERSION=v1.1.0` pulls the SkillEvaluator reports attached to that version.
7. Start over. `make reset` deletes SkillEvals and Jobs, removes the `beta` and `stable` tags and the extra versions from Harbor, and restores the model list.

## Make targets

| Target | What it does |
| --- | --- |
| `make up` | `pulumi up` of the reference stack: cluster, Harbor, secrets, project and webhook, toolbox, model list, Prometheus and Grafana |
| `make deploy` | `pulumi up` with the stack's skill-ci release: the CRDs and the controller from the OCI chart `oci://ghcr.io/dirien/charts/skill-ci-controller`, with the GHCR images of the same release (`TAG=0.8.1` sets the stack config `skillCiVersion` first) |
| `make sandbox-image` | Builds the sandbox image locally as `skill-ci-sandbox:local` (needs Docker) |
| `make seed` | Pushes the demo skills (`k8s-incident-triage`, `k8s-resource-naming`) as `v1.0.0` through the toolbox pod; `SEED_ALL=1` pushes every skill under `skills/`. Skips tags that exist. `RETRIGGER=1` re-fires the push webhook for existing tags |
| `make demo-push` | Pushes a new version of a skill (`SKILL=k8s-incident-triage VERSION=v1.1.0`) |
| `make demo-live` | The live push on stage: a small real edit of `k8s-incident-triage` v1.1.0 as a new version (`VERSION=v1.1.1`) |
| `make results` | The SkillEvals of one skill as a compact table (`SKILL=k8s-incident-triage`) |
| `make demo-model-swap` | Changes the model list live: `MODE=add` adds an optional model, `swap` replaces the Claude model, `bump [TO=2.1.308]` moves Claude Code to another release (the harness changed), `require AGENT=codex` / `optional AGENT=codex` flips whether an agent gates promotion, `restore` resets |
| `make status` | `kubectl get skillevals -o wide` plus tags and digests per skill from the Harbor API |
| `make report` | Downloads the eval reports attached to a skill version in Harbor (`SKILL=k8s-incident-triage VERSION=v1.1.0 [MODEL=...] OUT=reports/`) |
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
