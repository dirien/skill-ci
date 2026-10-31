# Changelog

All notable changes to skill-ci. The controller, runner, sandbox image and Helm chart share one version. The [GitHub releases](https://github.com/dirien/skill-ci/releases) carry the full list of commits, binaries, checksums and SBOMs of each version.

## [0.11.0](https://github.com/dirien/skill-ci/releases/tag/v0.11.0) (2026-10-31)

- New reporting UI: a React app embedded in the controller with overview, runs, skills, models and run pages, a trial session viewer (log with filters, tool calls, findings and eval criteria linked to steps, files, judge and network tabs) and a with/without compare view. The controller serves it with a read-only JSON API under `/api/v1/`; the server-rendered pages are gone.
- The runner stores every trial as a redacted trial bundle next to the report in Harbor, also for runs that fail after Tier 3 started. A trial in which a secret survives redaction is withheld.
- Network flows per trial from Hubble Relay (`spec.observability.hubbleRelay` in the SkillCIConfig).
- The LLM judge is configurable in the SkillCIConfig (`spec.judge`: anthropic, openai, openai-compatible or nv_build). Anthropic stays the default.
- Retries keep earlier attempts in `status.attempts`, failed runs keep their report, and pushes record the Harbor user as `skillci.dev/pushed-by`.
- The controller retries a run that failed only because an LLM provider rate-limited it.
- The README stands on its own, and the topic guides moved to `docs/`. skill-ci is documented as running on any conformant Kubernetes cluster, with the DOKS stack as one reference setup.
- The project is licensed under the Apache License 2.0 (`LICENSE`, `NOTICE`) and has community files: Code of Conduct, contributing guide, governance, maintainers, security policy, roadmap, adopters, issue and pull request templates, and a list of dependency licenses.
- New workflows check the DCO sign-off of every pull request commit and run the OpenSSF Scorecard.

## [0.10.1](https://github.com/dirien/skill-ci/releases/tag/v0.10.1) (2026-10-31)

- Fix: the network modes `none` and `allowlist` allow the judge host, so the Tier 3 verifier can reach it from the trial pod.

## [0.10.0](https://github.com/dirien/skill-ci/releases/tag/v0.10.0) (2026-10-31)

- New `SkillCIConfig` resource for the platform settings of an install.
- New `SandboxProfile` resource for the trial pod, and network modes `none`, `allowlist` and `full` that a skill declares in its metadata, enforced with Cilium network policies.
- Trials claim their pods from Agent Sandbox warm pools and carry eval labels and network metadata.
- A failed run records the tiers it completed.
- The Pulumi reference stack installs the controller from the OCI chart and applies the new CRDs, and `make deploy` runs `pulumi up` with the stack's release.

Version 0.9.0 was prepared but never tagged or released. Its changes are part of 0.10.0.

## [0.8.1](https://github.com/dirien/skill-ci/releases/tag/v0.8.1) (2026-10-31)

- The runner says what is missing when a skill has no eval dataset.
- `make draft-evals` drafts an `evals/evals.json` with SkillEvaluator for a person to review.
- `deploy/install.sh` and `make deploy` can turn on the OpenTelemetry export.

## [0.8.0](https://github.com/dirien/skill-ci/releases/tag/v0.8.0) (2026-10-31)

- The controller pushes the `skillci_*` metrics over OpenTelemetry (OTLP), configured through new chart values.
- Grafana dashboard fixes: the lift and its interval share one row, and the dashboard ConfigMap is replaced cleanly.

## [0.7.3](https://github.com/dirien/skill-ci/releases/tag/v0.7.3) (2026-10-31)

- The runner rejects an OpenRouter provisioning key before it starts any work.

## [0.7.2](https://github.com/dirien/skill-ci/releases/tag/v0.7.2) (2026-10-31)

- Fix: the runner forwards the gateway key to OpenCode in the sandbox.

## [0.7.1](https://github.com/dirien/skill-ci/releases/tag/v0.7.1) (2026-10-31)

- Fix: the runner allows the OpenCode gateway wrapper on the `ack` backend.
- The install path pins the sandbox image tag like the controller and runner tags, and upgrades Harbor only when its chart version or values change.
- The runner and sandbox images stay on Python 3.12.
- CI checks the plain Helm and kubectl install path: versions, generated manifests, rendering and validation.

## [0.7.0](https://github.com/dirien/skill-ci/releases/tag/v0.7.0) (2026-10-30)

First tagged release.

- The controller creates a `SkillEval` for every skill version pushed to Harbor, runs it as a Job, and moves the `beta` and `stable` tags with a statistical promotion and regression gate.
- The runner runs SkillEvaluator Tier 1, Tier 2 and Tier 3 in one `validate` run, with Claude Code, Codex and OpenCode as agents in the model list.
- One prebuilt sandbox image from CI replaces the per-task image builds in the cluster.
- A reporting site shows every run and its SkillEvaluator report.
- Helm chart for the controller, and an install path with plain Helm and Kubernetes manifests that needs no Pulumi.
- CI, linting and signed releases with GitHub Actions and GoReleaser v2.
