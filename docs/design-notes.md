# Design notes

Why skill-ci is built the way it is. Each note names a behaviour of an upstream part, or a problem found while building skill-ci, and the decision it led to.

## Trials and the sandbox

- Harbor framework's generic Kubernetes backend (`ack`) is thin. In DinD mode it ships only the Dockerfile to the build Job, so every `COPY` fails. Its context archive drops empty directories, which breaks the no-skill baseline. It has no network or resource policy support. `harbor-agent-sandbox` fixes the build context and swaps the privileged pod for an unprivileged Agent Sandbox `Sandbox`.
- SkillEvaluator hard-codes the Harbor framework environments and filters the environment variables it passes to them. The patch in `skillevaluator-patch/` adds `SKILLEVALUATOR_ACK_ENV_IMPORT_PATH` and lets `HARBOR_SANDBOX_IMAGE` (and the size variables) through. Both changes are candidates for upstream pull requests.
- The task image names of the Harbor framework say nothing about the skill version. SkillEvaluator names the task `skillevaluator-<eval case>-with-skill`, the `ack` backend turns that into `<registry>/skillevaluator-<eval case>-with-skill:latest`, and it builds only when an existence check says the image is missing. Two versions of one skill (or two skills with an eval case of the same name) share the image name, and the `COPY skills/` that carries the skill is baked into it. On the private registry of an earlier setup the check always answered "missing" (an unauthenticated `HEAD` gets 401, which the framework reads as absent, and the runner image has neither `docker` nor `crane`), so every trial rebuilt and pushed `:latest`. That hid the problem and cost a build per trial. On a registry that answers the check, a later version would be evaluated with an earlier version's skill. The sandbox image removes the build and with it the question: the skill is uploaded into the sandbox of each trial.
- For a plain skill (no `requirements.txt` or `apt-packages.txt`, no custom `evals/environment/Dockerfile`), SkillEvaluator 0.6.0 writes byte-identical Dockerfiles with and without the skill, for every skill and eval case. The skill is in the build context, not in the Dockerfile, and the three sample skills are plain. That is what makes one generic image possible. A skill with `requirements.txt` or a custom environment Dockerfile is not covered and fails with an error that names the step.
- The Harbor framework skips the agent install for Claude Code and Codex when `--version` matches the requested release, but OpenCode reinstalls nvm, Node and its npm package in every sandbox. `harbor_agent_sandbox.preinstalled` gives OpenCode the same check.
- SkillEvaluator rejects symlinked eval directories, so skills cannot be mounted from a ConfigMap. The runner pulls them with ORAS into an `emptyDir` instead, which matches the design of skills as OCI artifacts anyway.

## Agents and keys

- OpenCode behind a gateway needed two more patch hunks. SkillEvaluator refuses an agent import path outside `docker`, `kata` and `local` unless it is allow-listed, and the gateway OpenCode wrapper was not (hunk 6), so Tier 3 was skipped. The wrapper also renames the model to `skillevaluator-gateway/<id>`, a provider the Harbor framework does not know, so the key never reached the sandbox (hunk 12). `runner/check_patch.py` checks both when the image is built.
- Codex 0.161.0 with gpt-6.1-sol runs in code mode: every command is a JavaScript program sent to one `exec` tool. SkillEvaluator 0.6.0 decodes these programs with a small static grammar that misses the inline `text(await tools.exec_command({...}))` form Codex uses for most commands, single-quoted strings and `Promise.allSettled` batches. 140 of the 148 `exec` calls in our Codex runs were unsupported, so the verifier scored security 0.5 and could not evaluate skill efficiency and parts of skill execution. The trial score is the mean of five dimensions, so this pulled the measured Codex lift down to +0.13, against an estimated +0.46 with the calls decoded. Hunk 14 extends the grammar as proposed in [NVIDIA/SkillEvaluator#235](https://github.com/NVIDIA/SkillEvaluator/issues/235). It stays static, gives each call its own result, and leaves every other program unsupported. The adapter copies the normalizer into every task, so the verifier in the trial pod runs the patched code and the sandbox image needs no change. Drop the hunk once upstream fixes #235.
- An OpenRouter provisioning key looks valid and is not. It passes `GET /api/v1/key`, but every chat call answers 401 "User not found.". The runner asks for the key type before any work and fails the run early.

## The controller and the policy

- The controller deduplicates by digest, not tag. Otherwise every promotion would trigger a new evaluation.
- A lift above zero is not enough to promote. `acme-release-notes` v1.2.0 still scores +0.33 or more above no skill. Only the comparison with the stable version catches it, hence the regression gate.
- Verdicts depend on the other versions. Results arrive in any order (a SkillEval takes 11 to 13 minutes and they run three at a time), so the stable version can finish after the candidate. The controller re-runs the policy for the sibling SkillEvals of a skill when one succeeds or a tag moves, and an older version never takes `stable` from a newer one that is not worse. Without the second rule two equally good versions would swap the tag on every reconcile.
- Tag changes go through the Harbor REST API. `oras push` fires `PUSH_ARTIFACT`, and `oras tag` (a promotion) fires `PUSH_ARTIFACT` again with the same digest. Adding or deleting a tag through the Harbor API fires nothing, so the controller's tag changes send no webhooks.
- A fixed threshold is a poor gate for a noisy measurement. [gate.md](gate.md) explains why the gate uses confidence intervals.

## Installing Harbor

- The Harbor chart restarts Harbor on every `helm upgrade`. It renders new internal secrets each time, and their checksums are pod annotations. `deploy/install.sh` upgrades Harbor only when its chart version or values change.

## Verified end to end

These checks ran on DOKS clusters:

1. The Pulumi path. `pulumi up` on the reference stack, then `make deploy` installing each release from GHCR (0.7.0 to 0.7.3 in turn). A second `pulumi preview` showed no changes.
2. The Helm and kubectl path. On a fresh cluster: `deploy/install.sh` from the published OCI chart (about 20 minutes), re-runs that keep every Secret and skip an unchanged Harbor (under a minute), the upgrade from 0.7.0 to 0.7.2, a pushed skill evaluated end to end with the same numbers as the Pulumi cluster, then `deploy/uninstall.sh --yes --purge-crds` twice, leaving no namespace, release or volume. The script deletes the three skill-ci CRDs (`skillevals`, `sandboxprofiles` and `skillciconfigs`, which also removes the SkillCIConfig and SandboxProfiles that Helm keeps) and, with `--purge-crds`, the Prometheus Operator CRDs.
3. Three agents. Claude Code 2.1.292 + Opus 5.5, Codex 0.161.0 + gpt-6.1-sol and OpenCode 1.19.1 + Qwen 3.8 Flash (through OpenRouter) on every sample skill and variant, 9 trials per arm. The results are in [sample-skills.md](sample-skills.md).
4. Trials in Agent Sandbox. SkillEvaluator ran from a Kubernetes Job with in-cluster credentials, each trial was an Agent Sandbox, and no sandboxes were left behind.
