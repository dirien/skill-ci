# skill-ci contract

Shared interfaces for every part of skill-ci. Change this file first, then the code.

## Names

| Thing | Value |
| --- | --- |
| Namespace | `skill-ci` |
| Harbor (goharbor) in-cluster URL | `http://harbor.harbor.svc.cluster.local` (plain HTTP, also its `externalURL`) |
| Harbor project for skills | `skills` (repository = skill name) |
| Skill artifact | `oras push <host>/skills/<name>:vX.Y.Z --artifact-type application/vnd.agentskill.v1 <name>/` (directory named exactly like the skill) |
| Container images | `ghcr.io/dirien/<image>:<tag>`, built and pushed by the release workflow, one tag (the release, `0.7.0` for `v0.7.0`) for all three. Public packages, so no pull secret by default |
| Controller image | `ghcr.io/dirien/skill-ci-controller:<tag>` (linux/amd64, linux/arm64) |
| Runner image | `ghcr.io/dirien/skill-ci-runner:<tag>` (linux/amd64) |
| Sandbox image | `ghcr.io/dirien/skill-ci-sandbox:<tag>` (linux/amd64): the one image every trial sandbox starts from, see "Sandbox image" |

## Secrets (namespace `skill-ci`)

Nothing here is needed to pull the images while the GHCR packages are public. For private packages create a `docker-registry` Secret in the namespace and name it in the chart's `imagePullSecrets` (controller flag `--image-pull-secrets`): it is used for the controller, the runner Jobs and the sandboxes.

| Secret | Keys | Used by |
| --- | --- | --- |
| `llm-keys` | the judge's key (SkillCIConfig `spec.judge.secretKey`, default `anthropic`) and the key of each agent's route (`anthropic`, `openai`, `openai-base-url`, `nvidia`, `ollama`, `openrouter`; see "LLM keys") | runner Job |
| `harbor-credentials` | `username`, `password` (Harbor robot account or admin) | controller (Harbor REST API) |
| `harbor-pull` | `.dockerconfigjson` for `harbor.harbor.svc.cluster.local` | runner Job (oras pull) |

## Model list

ConfigMap `skill-ci-models`, key `models.yaml`:

```yaml
models:
  - agent: claude-code
    agentVersion: "2.1.292"         # optional: the release of the agent CLI
    model: claude-opus-5-5
    required: true                  # gates promotion
    sandboxProfile: default         # optional: the SandboxProfile of the trials (default: SkillCIConfig spec.defaultSandboxProfile)
  - agent: codex
    agentVersion: "0.161.0"
    model: openai/gpt-6.1-sol
    required: false                 # runs and is reported, never gates
  - agent: opencode
    agentVersion: "1.19.1"
    model: ollama-cloud/gpt-oss:120b
    required: false
```

`agent` is a SkillEvaluator/Harbor agent (`claude-code`, `codex`, `opencode`), the harness. `agentVersion` is the release of its CLI (letters, digits, `.`, `_`, `-`; at most 63 characters; it ends up in `npm install -g <package>@<version>` in the trial sandbox when it is not the default pin, so anything else is refused). **The identity of an entry is agent + agentVersion + model.** A changed `agentVersion` is a different entry, so the model-change trigger evaluates every `beta` and `stable` digest on it (the "harness changed" beat), and the old entry's runs stay as they are.

Without `agentVersion` the runner pins its default for the agent (see "Agent versions"), the SkillEval has no `spec.agentVersion` and the controller records the version that ran in `status.agentVersion`. An unpinned entry and a pinned one are different entries even when they end up on the same release; pin every entry to keep runs comparable.

`sandboxProfile` names the SandboxProfile (see "SandboxProfile") whose warm pool the entry's trials run in. It is not part of the identity: the controller copies it (or the SkillCIConfig `spec.defaultSandboxProfile` in force when the SkillEval is created, default `default`) into `spec.sandboxProfile` of every new SkillEval, and a change applies to new runs only.

`required` is the gating knob, per entry. Changing it on an existing entry (not a new one) updates `spec.required` of that entry's existing SkillEvals, which re-runs the tag policy for them: make an entry required and its results gate promotion of the versions already evaluated; make it optional and they stop gating. The runs of removed entries stay and keep gating while they are `required`, so delete them (or `make reset`) when you retire a required entry.

### Models per agent

| Agent | `model` | Provider route |
| --- | --- | --- |
| `claude-code` | a Claude model, `claude-opus-5-5` | `ANTHROPIC_API_KEY` |
| `codex` | an OpenAI model, `openai/<id>` (the prefix is removed for the CLI) | `OPENAI_API_KEY` and `OPENAI_BASE_URL` (default `https://api.openai.com/v1`) |
| `opencode` | `<provider>/<model id>`: `anthropic/...` runs natively with the Anthropic key; `ollama-cloud/...`, `openrouter/...`, `nvidia/...`, `openai/...` run through that provider's OpenAI-compatible endpoint with its key | see "LLM keys" |

The judge that scores every run, whatever the agent under test, is the SkillCIConfig `spec.judge` (default `anthropic`, `SKILL_EVAL_LLM_PROVIDER=anthropic`; see "SkillCIConfig" and "LLM keys").

## CRD: `SkillEval` (`skillci.dev/v1alpha1`, plural `skillevals`, short name `se`)

One object per skill digest x model list entry. Name: `<skill>-<version-with-dots-as-dashes>-<agent>[-<agent-version-with-dots-as-dashes>]-<model>` (the model slugged: lower case, other characters become dashes), for example `acme-release-notes-v1-1-0-claude-code-2-1-292-claude-opus-5-5`. A name longer than 63 chars is cut and ends in a 6-char hash of the full name. The agent and its version are part of the name because the same model runs under several agents and releases.

```yaml
spec:
  skill: acme-release-notes         # repository name in project "skills"
  version: v1.0.0                   # the semver tag that was pushed
  digest: sha256:...                # manifest digest; identity of the version
  agent: claude-code
  agentVersion: 2.1.292             # optional: omitted for an entry without agentVersion
  model: claude-haiku-5-5
  required: true
  attempts: 3                       # trials per arm (deployed default; one attempt is too noisy for the regression gate)
  trigger: push                     # push | model-change | manual
  sandboxProfile: default           # optional: copied from the model list entry; empty is the default profile
  network:                          # filled in by the controller before the run starts (see "Network modes")
    mode: allowlist                 # none | allowlist | full
    allow: ["@python", dl.k8s.io]   # optional: as declared, used only with allowlist
    source: skill                   # skill (declared) | default (SkillCIConfig spec.network.default)
status:
  phase: Pending                    # Pending | Running | Succeeded | Failed | Planned (dry run)
  jobName: ...                      # <skilleval name>, or <name>-r1, <name>-r2 for a retry
  agentVersion: 2.1.292             # the agent CLI release the trials ran (runner's report); spec's until then
  retries: 1                        # optional: fresh Jobs started after an infrastructure failure (see "Retry")
  withSkill: "0.95"                 # strings, two decimals
  withoutSkill: "0.36"
  lift: "+0.59"
  withSkillStdErr: "0.03"           # optional: standard error of the with-skill trial scores
  withoutSkillStdErr: "0.01"        # optional: same for the baseline
  n: "9/9"                          # optional: trials behind the scores, with/without
  liftCI: "[+0.41, +0.73]"          # optional: confidence interval of the lift at --confidence
  samples:                          # optional: per-trial scores the statistics come from
    withSkill: ["0.95", "0.90", "1.00"]
    withoutSkill: ["0.36", "0.38", "0.35"]
  verdict: Helps                    # Helps | Neutral | Hurts | Obsolete | Regressed | Inconclusive | Rejected
  reportDigest: sha256:...          # optional: the eval report attached to the skill digest in Harbor
  attempts:                         # optional: earlier Jobs of a retried run, oldest first (see "Retry")
    - jobName: <name>               # the Job of that attempt
      reportDigest: sha256:...      # optional: the report it attached (with its trial bundle)
      message: "EnvironmentStartTimeoutError: ..."   # why it ended, at most 1024 characters
  network:                          # optional: absent for runs made before network modes (they ran with full)
    mode: allowlist                 # the effective mode
    policy: skillci-net-<name>      # the CiliumNetworkPolicy, empty when nothing enforces the mode
    enforced: true
    hosts: [api.anthropic.com, dl.k8s.io, files.pythonhosted.org, pypi.org]   # TCP 443, bundles expanded, sorted
  sandboxProfile: default           # optional: the profile the run used; empty for a bare Sandbox per trial
  warmPool: default                 # optional: SANDBOX_WARM_POOL of the Job; empty for a bare Sandbox per trial
  message: ...
  completedAt: ...
```

Printer columns: Skill, Version, Agent (`spec.agent`), AgentVer (`status.agentVersion`), Model, Phase, With, Without, Lift, Verdict, Tier1, Network (`status.network.mode`), Age; Lift CI (`status.liftCI`), N (`status.n`), Publication and Report (`status.reportDigest`) at priority 1 (`-o wide`).

The statistics fields are present only when the runner sent trial scores (see "Statistics"). The controller recomputes them from `status.samples` whenever it reconciles a finished run, so a changed `--confidence` shows in `liftCI` without a new run. `samples` holds two decimals as strings (CRDs have no floats), up to 50 per arm.

`status.attempts` keeps the earlier Jobs of a run that was retried, so their reports and trial bundles stay reachable after `status.reportDigest` moves to the new attempt. The current Job is `status.jobName` and `status.reportDigest` and is not listed. At most 10 entries (the oldest go first).

Annotation `skillci.dev/pushed-by` on a SkillEval created by a skill push is the Harbor user who pushed the tag (`operator` of the webhook payload), when the payload has one; at most 253 characters, anything else is dropped. SkillEvals created by a model list change or by hand have none. It is shown as "started by" and is not used by the policy.

Labels on every SkillEval: `skillci.dev/skill`, `skillci.dev/digest` (hex without `sha256:`, first 32 chars), `skillci.dev/agent`, `skillci.dev/model`, and `skillci.dev/agent-version` (the value of `spec.agentVersion`; absent when the entry pins none). Dedupe is by digest + agent + agent version + model, the labels as a selector: an entry without a version matches only SkillEvals without the label, which includes the objects created before agent versions existed.

## Runner Job contract

The controller creates one `batch/v1` Job per SkillEval attempt, named like the SkillEval (a retry adds `-r1`, `-r2`, see "Retry"; cut short to fit 63 characters; the runner does not depend on the name) (owner reference to the SkillEval, `backoffLimit: 0`, `ttlSecondsAfterFinished: 3600`, ServiceAccount `skill-eval-runner`, imagePullSecrets from the controller flag `--image-pull-secrets`, none by default). Container `skill-eval` runs the runner image with no args and these env vars:

| Env | Example |
| --- | --- |
| `SKILL_REF` | `harbor.harbor.svc.cluster.local/skills/acme-release-notes@sha256:...` |
| `SKILL_NAME` | `acme-release-notes` |
| `AGENT` | `claude-code` |
| `AGENT_VERSION` | `2.1.292` (optional: set from `spec.agentVersion`; without it the runner uses its default pin for `AGENT`) |
| `MODEL` | `claude-haiku-5-5` |
| `ATTEMPTS` | `1` |
| `SANDBOX_IMAGE` | `ghcr.io/dirien/skill-ci-sandbox:0.7.0` (from the controller flag `--sandbox-image`, which defaults to the controller's own release; without it the runner uses the sandbox image of its own release) |
| `SANDBOX_PULL_SECRETS` | `ghcr` (comma separated Secret names for the sandbox pods; set from `--image-pull-secrets`, absent when there are none) |
| `ANTHROPIC_API_KEY` | from secret `llm-keys` key `anthropic`; required (`optional: false`) when the agent's route is Anthropic (`claude-code`, `opencode` `anthropic/...`), optional otherwise |
| `SKILL_EVAL_LLM_PROVIDER` | `anthropic`: the SkillCIConfig `spec.judge.provider` in force when the Job is created (see "SkillCIConfig"); always set |
| `SKILL_EVAL_LLM_MODEL` | `claude-opus-5-5`: `spec.judge.model`; absent when it is empty (SkillEvaluator rejects an empty value and uses its default model of the provider) |
| `SKILL_EVAL_LLM_BASE_URL` | `https://llm.example.com/v1`: `spec.judge.baseURL`; absent when it is empty |
| `SKILL_EVAL_LLM_API_KEY` | from secret `llm-keys` key `spec.judge.secretKey` (default `anthropic`), required: a Job whose judge key is missing does not start |
| `OPENAI_API_KEY` | from secret `llm-keys` key `openai` (optional) |
| `OPENAI_BASE_URL` | from secret `llm-keys` key `openai-base-url` (optional) |
| `NVIDIA_API_KEY` | from secret `llm-keys` key `nvidia` (optional) |
| `OLLAMA_API_KEY` | from secret `llm-keys` key `ollama` (optional) |
| `OPENROUTER_API_KEY` | from secret `llm-keys` key `openrouter` (optional). Must be an API key: before any work the runner asks OpenRouter (`GET /api/v1/key`) and fails the run when it is a provisioning key, which answers every chat call with 401 "User not found." |
| `SKILLEVAL_NAME` | `acme-release-notes-v1-1-0-claude-code-2-1-292-claude-opus-5-5`: always set, the SkillEval's `metadata.name` (also on a retry Job `<name>-r1`, whose name differs). The runner labels every trial pod `sandbox.users.io/skillci-eval=<SKILLEVAL_NAME>` and `sandbox.users.io/skillci-role=trial`, which the eval's CiliumNetworkPolicy selects, and `sandbox.users.io/skillci-trial=<trial label>`, which maps network flows to their trial (see "Trial bundles") |
| `SKILL_VERSION` | `v1.1.0`: the SkillEval's `spec.version`, written into the trial bundle index (optional: a runner without it leaves `version` out) |
| `HUBBLE_SERVER` | `hubble-relay.kube-system.svc.cluster.local:80`: the SkillCIConfig `spec.observability.hubbleRelay` in force when the Job is created; absent when it is empty. The runner then collects the trials' network flows (see "Trial bundles") |
| `SANDBOX_WARM_POOL` | `default`: the SandboxWarmPool of the run's SandboxProfile (`status.warmPool`). The runner claims one warm trial pod per trial (SandboxClaim with `warmPoolRef`) and ignores `SANDBOX_CPUS`/`SANDBOX_MEMORY_MB`, which the profile decides. Absent when the run has no profile or the profile has no warm pool (Agent Sandbox extensions not installed): the runner then starts a bare `Sandbox` per trial as before |

Pods and Jobs carry the labels `skillci.dev/skill`, `/digest`, `/agent`, `/model` and, for a pinned entry, `/agent-version`.

The runner image provides the rest as defaults (overridable env): `REGISTRY_CONFIG=/auth/config.json` (mount secret `harbor-pull` there), `REGISTRY_PLAIN_HTTP=true`, `SANDBOX_NAMESPACE=skill-ci`, `SANDBOX_IMAGE=ghcr.io/dirien/skill-ci-sandbox:<the runner's release>` (set when the runner image is built, from the release tag), `SANDBOX_CPUS=1`, `SANDBOX_MEMORY_MB=2048` (per-trial sandbox size), `TRIAL_CONCURRENCY=2` (trials at once per Job), `TRIAL_RETRIES=2` (how often Harbor runs a trial again that failed on a provider rate limit, `ApiRateLimitError`; `0` turns it off; see "Retry"), `TIMEOUT_MULTIPLIER=2` (scales Harbor's step timeouts; matters for a non-default `AGENT_VERSION`, which is installed in every sandbox), `TRIAL_BUNDLE=on` (`off` attaches no trial bundle), `TRIAL_BUNDLE_MAX_BYTES=25165824` (compressed size of the whole bundle) and `TRIAL_OUTPUT_MAX_BYTES=8192` (the cap on every observation of a trial that is over its size limit; see "Trial bundles"). Sandboxes created by a runner pod are owned by that pod, so cancelling a run deletes them. No registry, no pull secret and no builder are part of the defaults: see "Sandbox image" for the one optional path that uses them (`TASK_IMAGE_REGISTRY`).

### Sandbox image

No image is built while an eval runs. SkillEvaluator writes a Dockerfile for every task (eval case and arm), but for a plain skill those Dockerfiles are the same list of steps: `FROM python:3.12-slim`, `apt-get install bash curl git jq ripgrep`, one `pip install` of the verifier packages and an import smoke test, `mkdir` of `/workspace` and `/logs`, and a series of evaluator-owned resets of the skill directories. What differs per task is the build context: `COPY skills/ ...` (the skill under test for the with-skill arm, an empty directory for the baseline), `COPY input/` and `COPY repo/` when the case has them, the final `WORKDIR` and the `ENV` resets. `sandbox/Dockerfile` therefore builds **one generic image** with all the generic steps, and the runner starts every trial sandbox from it:

- **Contents.** Python 3.12 (Debian bookworm), the tools above plus `procps` and `coreutils`, the verifier packages (the `pip` and smoke-test steps are copied verbatim from the generated Dockerfile; a test compares them), the workspace directories, and the three agent CLIs at the default pins of "Agent versions", installed the way Harbor installs them (Claude Code: native installer into `~/.local/bin`; Codex and OpenCode: `npm` under nvm Node 22 in `~/.nvm`, with `node`, `codex` and `opencode` linked into `/usr/local/bin`). `sandbox/install-agents.sh` fails the image build when a CLI reports another release.
- **Task-specific steps are replayed at start** by `harbor_agent_sandbox.AgentSandboxEnvironment` (env `HARBOR_SANDBOX_IMAGE`, set by the runner from `SANDBOX_IMAGE`). It parses the task Dockerfile and, once the sandbox is ready: uploads every `COPY`/`ADD` source of the build context to its destination with Harbor's `upload_dir`/`upload_file` and sets owner (root, or `--chown`) and mode (`--chmod`) as `COPY` would; takes the last `WORKDIR` as the container's `workingDir`, every `ENV` as container env and `USER` (root or a uid) as `runAsUser`. The sandbox pod's `imagePullPolicy` is `IfNotPresent` for a tag other than `latest`.
- **A step the image lacks fails the trial, visibly.** The `FROM` must be `python:3.12-slim` and every `RUN` must be one of the steps in `harbor_agent_sandbox/dockerfile.py` (`COVERED_RUNS`, digests of the normalized commands; the test `test_covered_runs_are_exactly_the_generated_ones` ties them to a real generated Dockerfile). A skill with `requirements.txt` or `apt-packages.txt`, a custom `evals/environment/Dockerfile`, `ARG`, multi-stage builds, remote `ADD` or `USER <name>` raise an error before any pod is created: `task <name> cannot run on <image>: the task Dockerfile needs N step(s) the prebuilt sandbox image does not have: line 8: RUN python -m pip install ...`. The run ends `Failed` with that message (it is not retried). The fix is to add the step to `sandbox/Dockerfile` and its digest to `COVERED_RUNS`, or to build task images yourself (below). After a SkillEvaluator upgrade, `python -m harbor_agent_sandbox.dockerfile digests <generated Dockerfile>` prints the digests to compare.
- **Order.** The covered `RUN`s are in the image, so they happen before every `COPY`. That is the same result as the generated Dockerfile because those steps only reset directories that are empty in a fresh container. Empty directories inside a `COPY` source are not created, like Harbor's `upload_dir`.
- **Agent CLIs are not installed per trial.** Claude Code and Codex check `claude --version` / `codex --version` against the requested release and skip their install when it matches. Harbor's OpenCode has no such check and always reinstalls nvm, Node and the package, which is what exceeded the agent setup timeout (`AgentSetupTimeoutError`) on a busy cluster. `harbor_agent_sandbox.preinstalled` wraps `OpenCode.install` (applied when the package is imported) so it returns after one `opencode --version` when the release matches. An `AGENT_VERSION` other than the default pin (the "harness changed" beat) is installed in the sandbox as before, so it is slower and `TIMEOUT_MULTIPLIER` matters.
- **Bring your own builder (optional).** Setting `TASK_IMAGE_REGISTRY=<registry/path>` (and `TASK_IMAGE_PULL_SECRET=<secret>` if it needs credentials) on the runner switches the environment back to Harbor's `ack` build: each task image is built and pushed to that registry (a `docker:dind` Job, or the BuildKit named by `HARBOR_BUILDKIT_ADDRESS`, which needs `buildctl` in a runner image of your own) and `SANDBOX_IMAGE` is not used. The runner then needs `get` on that Secret (Helm `runner.pullSecretName`). Mind that Harbor names a task image `<registry>/<environment_name>:latest`, where the name holds the eval case and the arm but neither the skill nor its version, and that it builds only when its existence check says the image is missing: with a registry that answers that check (public, or `crane` or `docker` available) a later version of a skill is evaluated with the skill content of the first one built. Delete the images between versions, or do not use this path. skill-ci does not run it by default.

Runner pods and trial sandbox pods carry `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"`, so a node scale-down never kills a running eval. The container sets `terminationMessagePolicy: FallbackToLogsOnError`. On completion it writes one JSON object to `/dev/termination-log` and exits 0:

```json
{"withSkill": 0.95, "withoutSkill": 0.36, "lift": 0.59, "agent": "claude-code", "model": "claude-haiku-5-5",
 "agentVersion": "2.1.292", "report": "sha256:..."}
```

`agentVersion` is optional: the agent CLI release the trials ran (see "Agent versions"; a rejected run, where no trial ran, reports the requested release). The controller keeps it in `status.agentVersion` when it looks like a version (letters, digits, `.`, `_`, `-`) and ignores anything else. A message without it (an older runner) leaves `status.agentVersion` at the requested release or empty.

`samples` is optional: the overall score of every trial, per arm, two decimals (see "Statistics"):

```json
{"withSkill": 0.95, "withoutSkill": 0.36, "lift": 0.59, "agent": "claude-code", "model": "claude-haiku-5-5",
 "samples": {"withSkill": [0.95, 0.95, 0.95, 0.95, 0.95, 0.95, 0.95, 0.95, 0.95],
             "withoutSkill": [0.36, 0.38, 0.35, 0.30, 0.40, 0.35, 0.36, 0.37, 0.38]}}
```

The runner reads them from SkillEvaluator's `result.json`: `agents.<agent>.pass_at_k.<with_skill|without_skill>.cases.<case>.attempts[].score`, one entry per attempt of every eval case, whose mean is the arm's `overall_*` score. With 3 cases x 3 attempts that is 9 scores per arm. The runner caps each arm at 50 scores, and leaves `samples` out when either arm has none (an older SkillEvaluator). A termination message without `samples`, or with fewer than two scores in an arm, is valid; the controller then uses the fixed-number rules.

`report` is optional: the digest of the report the runner attached to the evaluated skill digest (see "Eval reports"). The controller stores it in `status.reportDigest`. A runner that cannot attach leaves the field out; the eval still succeeds. `report` is also allowed next to `error` (below): when Tier 3 started and then failed, the runner attaches what it has, and the controller stores the digest in `status.reportDigest` of the `Failed` run the same way (a retry moves it to `status.attempts`).

If no score was produced it writes `{"error": "<reason>"}` and exits 1. When the error comes after the Tier 1 + 2 gate ran, the payload keeps the gate: `{"error": "...", "tiers": {"tier1": "passed", "tier2": "passed", "tier3": "skipped"}, "findings": [...]}`, and, when Tier 3 started and the report could be attached, `"report": "sha256:..."` (`tier3` is `skipped` when the live trials never started, `incomplete` when they started and failed; `publication` may be present too; findings at most 5, redacted). Both fields are optional; the controller stores them in `status.tiers`, `status.publication` and `status.findings` of the `Failed` run, so the reporting site shows the gate. A skill without a Tier 3 source (none of `evals/evals.json`, `evals.jsonl`, `evals.yaml`, `evals.yml`, `dataset.json`, `dataset.jsonl`, and no `evals/harbor/` tasks) gets the Tier 1 + 2 gate only; when the gate passes, the runner starts no Tier 3 run and writes ``{"error": "no eval dataset: the live evaluation (Tier 3) needs test cases in evals/evals.json. Draft them with `make draft-evals SKILL=<skill>` (skillevaluator create-eval-dataset), review them, and push again"}`` (the gate's tiers and findings go to the container log). A Tier 1 or 2 failure is reported as Rejected, as for any skill. The runner never generates or rewrites a dataset (`validate --no-autopilot`): the gate runs the test cases the author committed.

The `skill-eval-runner` ServiceAccount and Role are in `runner/job.yaml` today; the controller manifests own them from now on.

### Agent versions

The agent CLI is part of the sandbox image at the default pin below (an `AGENT_VERSION` that differs is installed in the sandbox). Its release is always pinned, so trials do not change when a CLI ships:

| Agent | Default pin (no `AGENT_VERSION`) | Source |
| --- | --- | --- |
| `claude-code` | `2.1.292` | SkillEvaluator 0.6.0 (`_CLAUDE_CODE_VERSION`) |
| `codex` | `0.161.0` | SkillEvaluator 0.6.0 (`_CODEX_VERSION`) |
| `opencode` | `1.19.1` | skill-ci: SkillEvaluator pins none, so Harbor would install the latest release in every trial. 1.19.1 (npm `opencode-ai`, GitHub `anomalyco/opencode` v1.19.1) was published on 2026-10-15, at least two weeks before it was chosen (2026-10-30) |

The runner passes the release to SkillEvaluator as `SKILLEVALUATOR_AGENT_VERSION`; `skillevaluator-patch/` hunk 7 makes `_harbor_agent_kwargs` use it for Harbor's `version` agent kwarg (replacing the pin of claude-code and codex, setting one for opencode). A runner test compares the claude-code and codex defaults with the installed SkillEvaluator's pins.

The release that ran is read from Harbor's trial results (`<run>/_harbor-jobs/<job>/<trial>/result.json`, `agent_info.version`) and reported as `agentVersion` in the termination message; when the jobs were not kept it is the requested release.

### LLM keys

The judge's key is required, whatever the provider: the controller mounts `llm-keys/<spec.judge.secretKey>` as `SKILL_EVAL_LLM_API_KEY` (not optional, so a Job without it does not start), and the runner fails before any work when the judge has no key. Each agent needs the key of its own route too: Claude Code and OpenCode `anthropic/...` `ANTHROPIC_API_KEY` (llm-keys `anthropic`, required in their Jobs), Codex `OPENAI_API_KEY` (llm-keys `openai`), an OpenCode gateway model its provider's key (below), unless the judge is of the agent's own provider, whose key SkillEvaluator then gives the agent. A Job without `SKILL_EVAL_LLM_PROVIDER` (an older controller) gets `anthropic` whenever it has an Anthropic key.

SkillEvaluator 0.6.0 reads the judge's key from a variable per provider; the runner copies `SKILL_EVAL_LLM_API_KEY` into that variable when it is unset, and the trial bundle index names the variable SkillEvaluator reads (`credentials`, role `judge`):

| `SKILL_EVAL_LLM_PROVIDER` | Key SkillEvaluator reads | Base URL |
| --- | --- | --- |
| `anthropic` | `ANTHROPIC_API_KEY` only (`SKILL_EVAL_LLM_API_KEY` is copied there) | `SKILL_EVAL_LLM_BASE_URL`, else `ANTHROPIC_BASE_URL`, else `https://api.anthropic.com` |
| `openai` | `OPENAI_API_KEY`, then `SKILL_EVAL_LLM_API_KEY` | `SKILL_EVAL_LLM_BASE_URL`, else `OPENAI_BASE_URL`, else `https://api.openai.com/v1` |
| `openai-compatible` | `SKILL_EVAL_LLM_API_KEY` (required) | `SKILL_EVAL_LLM_BASE_URL` (required) |
| `nv_build` | `NVIDIA_API_KEY` only (`SKILL_EVAL_LLM_API_KEY` is copied there) | `https://integrate.api.nvidia.com/v1` |

SkillEvaluator also knows `bedrock` (the AWS credential chain, `AWS_REGION`, default `us-west-2`); skill-ci does not offer it, because no agent can be scored with it.

**Agent and judge.** SkillEvaluator chooses the agent's credentials from the judge provider, so the runner accepts only these combinations and fails the run before any work otherwise; the controller checks the same table when it resolves a `Pending` run (see "SkillCIConfig"):

| Agent | Judge providers |
| --- | --- |
| `claude-code` | `anthropic`, `openai`, `openai-compatible`, `nv_build` |
| `codex` | `anthropic`, `openai`, `nv_build` |
| `opencode` | `anthropic` |

SkillEvaluator hands the agent the credentials of its own route. Under a judge of the agent's own provider (Claude Code under `anthropic`, Codex under `openai`) that is the judge's key and endpoint, so the agent and the judge share them; under any other judge the agent never sees the judge's key.

- **Codex** needs `OPENAI_API_KEY` and `OPENAI_BASE_URL` together (SkillEvaluator refuses a partial pair under an `anthropic` judge). The runner sets `OPENAI_BASE_URL=https://api.openai.com/v1` when only the key is present. The model is passed as given (`--agent-model codex=openai/<id>`) and must be one the key can use; SkillEvaluator checks it against `<base url>/models`.
- **OpenCode** with an Anthropic judge accepts only `anthropic/<model>` in stock SkillEvaluator 0.6.0. For `ollama-cloud/...`, `openrouter/...`, `nvidia/...` and `openai/...` models the runner uses the OpenAI-compatible gateway route that `skillevaluator-patch/` hunks 6 and 8 to 12 open: it presents the provider's key as `OPENAI_API_KEY` and its endpoint (`https://ollama.com/v1`, `https://openrouter.ai/api/v1`, `https://integrate.api.nvidia.com/v1`, `OPENAI_BASE_URL` or `https://api.openai.com/v1`) as `OPENAI_BASE_URL` to the agent only, runs the model as `openai/<id>` with SkillEvaluator's `SkillEvaluatorGatewayOpenCode` (Chat Completions through `@ai-sdk/openai-compatible`; hunk 6 allows that wrapper on the `ack` backend and hunk 12 forwards the key into the sandbox, which Harbor does not do for the wrapper's `skillevaluator-gateway/` model namespace), and keeps the real OpenAI or NVIDIA key for Tier 2 embeddings through `SKILL_EVAL_EMBEDDING_PROVIDER=openai-compatible` (or `nv_build`). A missing key for the provider fails the run before any work with `opencode model ... needs <KEY>`.
- A termination message and a log never contain a key: the runner redacts the values of all of the keys above, `SKILL_EVAL_LLM_API_KEY`, `SKILL_EVAL_EMBEDDING_API_KEY` and `AWS_BEARER_TOKEN_BEDROCK`.

## SkillCIConfig (`skillci.dev/v1alpha1`, plural `skillciconfigs`, short name `scc`)

The platform settings of one skill-ci install: the default SandboxProfile, the network policy of the trial sandboxes, the Hubble Relay of the network flows and the LLM judge. Namespaced; the controller reads only the object named `default` in its own namespace (one install per namespace). An object with another name gets `Ready` `False`, reason `Ignored`, and a Warning event, and changes nothing. Printer columns: Default-Network, Enforcement (resolved: `cilium` or `none`), Default-Profile, Judge (`status.effective.judge.provider`), Ready, Age (from `status.effective`, the settings in force).

```yaml
apiVersion: skillci.dev/v1alpha1
kind: SkillCIConfig
metadata:
  name: default
  namespace: skill-ci
spec:
  defaultSandboxProfile: default    # SandboxProfile of a model list entry without sandboxProfile (default "default")
  network:
    default: full                   # none | allowlist | full (default full: the internet, never the cluster)
    modes: [none, allowlist, full]  # allowed modes, a set of 1 to 3 (default all three); must contain default
    enforcement: auto               # auto | cilium | off (default auto)
    alwaysAllow: []                 # hosts added to none and allowlist, at most 20
    bundles:                        # name -> hosts, named @<name> in skillci.dev/network-allow; at most 32, 1 to 20 hosts each
      apt: [deb.debian.org, security.debian.org]
      github: [github.com, api.github.com, codeload.github.com, objects.githubusercontent.com]
      node: [registry.npmjs.org]
      python: [pypi.org, files.pythonhosted.org]
    agentInstallHosts: [registry.npmjs.org]   # added for an agent version not in --preinstalled-agents, at most 20
  observability:
    hubbleRelay: ""                 # host:port of Hubble Relay; empty (default) collects no network flows
  judge:                            # the LLM judge of every run, whatever the agent under test
    provider: anthropic             # anthropic | openai | openai-compatible | nv_build (default anthropic)
    model: ""                       # empty (default): SkillEvaluator's default model of the provider
    baseURL: ""                     # API root; required for openai-compatible, not allowed for nv_build
    secretKey: anthropic            # the llm-keys key that holds the judge's API key (default anthropic)
status:
  observedGeneration: 3
  conditions:                       # Ready: Applied | Invalid | Ignored
    - type: Ready
      status: "True"
      reason: Applied
      message: "applied: default network full, allowed modes none, allowlist, full, enforcement auto (cilium), bundles @apt, @github, @node, @python, default sandbox profile default, hubble relay hubble-relay.kube-system.svc.cluster.local:80, judge anthropic (default model, key llm-keys/anthropic)"
  effective:                        # what is in force
    source: SkillCIConfig           # SkillCIConfig | LastValid | BuiltIn
    generation: 3                   # generation of the spec in force (0 for BuiltIn)
    defaultSandboxProfile: default
    network:
      default: full
      modes: [none, allowlist, full]
      enforcement: auto
      enforcedBy: cilium            # cilium | none: what enforcement resolves to on this cluster
      bundles: [apt, github, node, python]
      agentInstallHosts: [registry.npmjs.org]
    observability:
      hubbleRelay: hubble-relay.kube-system.svc.cluster.local:80
    judge:
      provider: anthropic
      secretKey: anthropic
      host: api.anthropic.com       # the judge host of none and allowlist
```

`spec.observability.hubbleRelay` is where the runner reads network flows (see "Trial bundles"): `host:port` of a Hubble Relay the runner pods can reach (a hostname or IP, a port from 1 to 65535, at most 255 characters; no scheme). The controller passes it to every new runner Job as `HUBBLE_SERVER`; empty collects nothing, and the trial bundles record network `off`. The chart seeds it from `config.observability.hubbleRelay` (default empty); `infra/` and `deploy/install.sh` set `hubble-relay.kube-system.svc.cluster.local:80` when they find Cilium with Hubble Relay (DOKS). Runner pods have no egress policy, so they reach the relay on any cluster; only trial pods are restricted. A relay that cannot be reached never fails a run.

`spec.judge` is the LLM judge that scores every trial, whatever agent is under test. `provider` is one of the providers SkillEvaluator 0.6.0 supports that an agent can be scored with (not `bedrock`, see "LLM keys"). The controller passes the judge in force to every new runner Job as `SKILL_EVAL_LLM_PROVIDER`, `SKILL_EVAL_LLM_MODEL`, `SKILL_EVAL_LLM_BASE_URL` and `SKILL_EVAL_LLM_API_KEY` (see "Runner Job contract"); a Job keeps the judge it was created with, so a change applies to the runs that start after it, a retry included. The judge's host is allowed in the network modes `none` and `allowlist` (see "Network modes", "Judge host"): the host of `baseURL` when it is set, else `api.anthropic.com` (anthropic), the host of `llm-keys` `openai-base-url` or `api.openai.com` (openai; SkillEvaluator falls back to `OPENAI_BASE_URL`), `integrate.api.nvidia.com` (nv_build). The runner hands the key to SkillEvaluator under the variable the provider reads (see "LLM keys"). The controller checks the agent and judge table of "LLM keys" when it resolves a `Pending` run: another combination ends `Failed` before any Job with `judge provider "<p>" cannot score agent "<a>": the runner supports the judge providers <list> for it (SkillCIConfig spec.judge)`. The chart seeds the judge from `config.judge`. Scores of different judges are not comparable; the controller records no judge per run.

**Validation.** The CRD checks the enums, the host pattern (lowercase RFC 1123 hostname with at least two labels, optional leading `*.`, at most 255 characters), the bundle name pattern (`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`, at most 63 characters), the list and map sizes, that `network.default` is one of `network.modes` (CEL), the `observability.hubbleRelay` pattern, and the judge: `model` (at most 256 letters, digits and `._:/@+-`), `baseURL` (`http` or `https`, no whitespace, credentials, query or fragment, at most 2048 characters), `secretKey` (a Secret key, at most 253 characters), `baseURL` required for `openai-compatible` and not allowed for `nv_build` (CEL). The API server fills in the defaults above for fields left out; the controller applies the same defaults to fields it reads as absent. The controller validates again (the same rules, for objects stored before a CRD upgrade added a check or applied without validation) and reports a spec it cannot use with `Ready` `False`, reason `Invalid`, the message `invalid spec: <what is wrong>; the controller keeps the last valid spec (generation <n>)` and a Warning event. Without a valid spec of the same object seen since the controller started (after a restart, or a new object) the message ends `the controller uses the built-in defaults (...)`. With `enforcement: cilium` and no Cilium CRD, `Ready` stays `True` and the message adds that every run fails until Cilium is installed.

**Precedence.** A valid SkillCIConfig `default` > the last valid spec of that object (while the current one is invalid; kept in memory, not across a restart) > the built-in defaults (the values above). A cluster without the SkillCIConfig CRD (an upgrade that did not apply `crds/`), or without the object, runs on the built-in defaults; the controller logs it at start. Deleting the object brings back the built-in defaults.

**Live updates.** No restart: the controller reads the object through the API server whenever it resolves a `Pending` run (see "Network modes", "When a change applies"), when the webhook or a model list change creates SkillEvals (`defaultSandboxProfile` is copied into `spec.sandboxProfile` then), and when it reconciles a SandboxProfile. It watches SkillCIConfigs to keep the status current (and every 5 minutes, since Cilium may be installed later) and to reconcile every SandboxProfile again when the config changes, because `enforcement` decides the template's `networkPolicyManagement` and the baseline policy.

**Ownership.** The Helm chart creates `default` from `config.*` (`config.create: true`) only when it does not exist yet (`lookup`; rendered with `helm.sh/resource-policy: keep`) and never renders or deletes it afterwards, so edits made with `kubectl -n skill-ci edit scc default` or GitOps are never overwritten by an upgrade. `helm template` has no cluster and always renders it. `config/manager/skillciconfig.yaml` is the kustomize copy (there, the file is the source of truth). RBAC: `get`, `list`, `watch` on `skillciconfigs`, `get`, `update`, `patch` on `skillciconfigs/status`.

## SandboxProfile (`skillci.dev/v1alpha1`, plural `sandboxprofiles`, short name `sbp`)

The kind of trial sandbox a model list entry runs in. Namespaced, in the controller namespace. Printer columns: Image, CPU, Memory, Warm (`spec.warmPool.replicas`), Ready (`status.readyReplicas`), Age.

```yaml
apiVersion: skillci.dev/v1alpha1
kind: SandboxProfile
metadata:
  name: default
spec:
  image: ""                         # optional: empty is the controller's --sandbox-image
  resources:
    cpu: "1"                        # request only (default 1)
    memory: 2Gi                     # request and limit (default 2Gi)
  warmPool:
    replicas: 2                     # warm, unclaimed trial pods (default 2, minimum 0; 0 cold-starts each trial in about 1 s)
  runtimeClassName: gvisor          # optional
  nodeSelector: {pool: trials}      # optional
  tolerations: []                   # optional
status:
  templateName: default             # the SandboxTemplate
  warmPoolName: default             # the SandboxWarmPool, SANDBOX_WARM_POOL of the runner Job
  readyReplicas: 2
  conditions:                       # Ready: WarmPoolReady | WarmPoolStarting | AgentSandboxExtensionsMissing
    - {type: Ready, status: "True", reason: WarmPoolReady, message: 2 of 2 warm trial pods ready}
```

The controller reconciles every profile into two objects of the same name, owned by the profile (`extensions.agents.x-k8s.io/v1beta1`, Agent Sandbox extensions):

- **SandboxTemplate**: `podTemplate.metadata.labels` `skillci.dev/component: trial-sandbox` and `skillci.dev/profile: <name>`, annotation `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"`; pod `automountServiceAccountToken: false`, `terminationGracePeriodSeconds: 1`, one container `main` with the image, `command: ["sleep", "infinity"]`, `securityContext {runAsUser: 0, allowPrivilegeEscalation: false}` (the same container as the bare Sandbox of `harbor_agent_sandbox`), `imagePullPolicy` `IfNotPresent` (`Always` for `latest` or no tag), the resources, `runtimeClassName`/`nodeSelector`/`tolerations` when set and `imagePullSecrets` from `--image-pull-secrets`. `networkPolicyManagement` is `Unmanaged` when Cilium enforces network modes (skill-ci's policies own egress) and `Managed` otherwise (Agent Sandbox's default: the internet, not the cluster).
- **SandboxWarmPool**: `{replicas, sandboxTemplateRef: {name}, updateStrategy: {type: Recreate}}`, so a template change (a new image after a release upgrade) replaces stale warm pods.

`status.readyReplicas` comes from the warm pool's status; Ready is `True` when every warm pod is ready. The controller watches the owned objects when their CRDs exist at start and re-checks every profile every 5 minutes (every 30 s when not watching). Without the Agent Sandbox extensions CRDs the profile reports Ready `False`, reason `AgentSandboxExtensionsMissing`, and runs start a bare `Sandbox` per trial (no `SANDBOX_WARM_POOL`).

**Where the settings live.** Profiles are objects: edit them with `kubectl -n skill-ci edit sbp <name>` or GitOps. The chart seeds `default` from Helm `sandboxProfiles.default` (`warmPool.replicas: 2`, `resources: {cpu: "1", memory: 2Gi}`, empty image) **only when it does not exist yet** (Helm `lookup`; rendered with `helm.sh/resource-policy: keep`), and never renders or deletes it again, so later value changes do not overwrite edits; more profiles via `sandboxProfiles: {<name>: <spec>}` (same create-once rule, `create: false` skips one) or kubectl. A profile chart 0.9.0 created without the annotation is rendered once more from its live spec with the annotation, so the 0.10.0 upgrade adopts it. `config/` (kustomize) ships `manager/sandboxprofile.yaml`, which `kubectl apply -k` keeps in line with the file. Which profile an entry without `sandboxProfile` uses is the SkillCIConfig `spec.defaultSandboxProfile` (see "SkillCIConfig"); the sandbox image of an empty `image` is the controller flag `--sandbox-image`.

A SkillEval that names a profile (`spec.sandboxProfile`, copied from the model list) that does not exist ends `Failed` with `sandbox profile "<name>" not found` and no Job. A SkillEval without `spec.sandboxProfile`, or one on the SkillCIConfig `defaultSandboxProfile` while that profile does not exist (an install without it), runs a bare `Sandbox` per trial.

## Network modes

| Mode | Trials may reach |
| --- | --- |
| `full` | the internet, not the cluster |
| `allowlist` | the model API host(s), the judge host (`api.anthropic.com` by default, see "Judge host"), the hosts the skill declares and the SkillCIConfig `network.alwaysAllow` hosts, TCP 443 |
| `none` | the model API host(s), the judge host (`api.anthropic.com` by default) and the SkillCIConfig `network.alwaysAllow` hosts, TCP 443 |

**Declaration.** In `SKILL.md` frontmatter `metadata`: `skillci.dev/network: none | allowlist | full` and, for `allowlist`, `skillci.dev/network-allow: "@python,@github,dl.k8s.io"`. A host is a lowercase RFC 1123 hostname with an optional leading `*.`; entries are comma separated; `@<name>` is a bundle; at most 20 hosts after bundle expansion. `demo/push-skill.sh` copies both keys into OCI manifest annotations with the same names; without it, push with `oras push --annotation skillci.dev/network=allowlist --annotation skillci.dev/network-allow=@python ...`. Before a SkillEval's first Job the controller reads the annotations of its digest from the Harbor REST API (`GET /api/v2.0/projects/<project>/repositories/<skill>/artifacts/<digest>`, field `annotations`) into `spec.network`; a spec that already has `network` is not looked up. **A digest without a `skillci.dev/network` annotation gets mode `full` (`source: default`), because the SkillCIConfig `network.default` defaults to `full`.** `network-allow` next to `none` or `full` is ignored, and the NetworkPolicy condition message says so.

**Bundles** (SkillCIConfig `network.bundles`, defaults): `python: pypi.org, files.pythonhosted.org`; `node: registry.npmjs.org`; `github: github.com, api.github.com, codeload.github.com, objects.githubusercontent.com`; `apt: deb.debian.org, security.debian.org`. Setting `bundles` replaces the defaults.

**Model API hosts** (always allowed with `none` and `allowlist`, the table of `runner/skill_eval.py` `GATEWAY_PROVIDERS`): `claude-code`, and `opencode` `anthropic/...`: `api.anthropic.com`; `codex`, and `opencode` `openai/...`: the host of `llm-keys` `openai-base-url` when set, else `api.openai.com`; `opencode` `openrouter/...`: `openrouter.ai`, `ollama-cloud/...`: `ollama.com`, `nvidia/...`: `integrate.api.nvidia.com`. **Judge host.** The host of the SkillCIConfig `spec.judge` in force when the run is resolved is always allowed with `none` and `allowlist`, whatever the agent: SkillEvaluator stages the LLM judge's credentials into the Harbor verifier, and the verifier runs inside the trial pod. It is the host of `judge.baseURL` when that is set, else `api.anthropic.com` for `anthropic` (the default), the host of `llm-keys` `openai-base-url` or `api.openai.com` for `openai`, `integrate.api.nvidia.com` for `nv_build`. Under the `anthropic` judge Claude Code and OpenCode `anthropic/...` use the judge's endpoint as well, which this host covers. An entry whose agent version is not in `--preinstalled-agents` also gets the SkillCIConfig `network.agentInstallHosts` (Harbor installs the CLI with npm). An entry without `agentVersion` runs the preinstalled default pin.

**Where the settings live.** Platform policy is the SkillCIConfig `default` (see "SkillCIConfig"):

| Field | Default | Meaning |
| --- | --- | --- |
| `spec.network.default` | `full` | Mode of a skill that declares none |
| `spec.network.modes` | `[none, allowlist, full]` | Modes a skill may declare |
| `spec.network.enforcement` | `auto` | `auto`: CiliumNetworkPolicies when the CRD `ciliumnetworkpolicies.cilium.io` exists (looked up through the API, cached for a minute); `cilium`: require it; `off`: no policies, every mode runs unenforced |
| `spec.network.alwaysAllow` | empty | Hosts added to `none` and `allowlist` |
| `spec.network.bundles` | the bundles above | Bundles a skill names as `@<name>` |
| `spec.network.agentInstallHosts` | `[registry.npmjs.org]` | Hosts an agent install needs |

One controller flag stays, because it is a fact of the release image rather than a policy: `--preinstalled-agents` (default `claude-code@2.1.292,codex@0.161.0,opencode@1.19.1`, Helm `controller.preinstalledAgents`), the agent pins in the sandbox image. The flags `--network-default`, `--network-modes`, `--network-enforcement`, `--network-always-allow`, `--network-bundles`, `--agent-install-hosts` and `--default-sandbox-profile` of 0.9.0 are gone (an old argument list makes the controller exit with `flag provided but not defined`).

**When a change applies.** The controller reads the SkillCIConfig when it resolves a `Pending` run, right before its first Job. A run resolved after an edit uses the new settings; a run that is already `Running` or finished keeps its `spec.network` and its policy. The mode in `spec.network` is recorded once (`source: default` takes the `network.default` in force then) and is not changed by a later edit of `network.default`, but the allowed modes, bundles, hosts and enforcement are those in force when the run is resolved, so a recorded mode the platform no longer allows fails with `NotAllowed`.

**Runs that cannot start** end `Failed` before any Job, with condition `NetworkPolicy` `False` and the reason:

| Reason | Message |
| --- | --- |
| `NotAllowed` | `network mode "<m>" is not allowed on this platform (allowed: <list>)` (also for a value that is no mode) |
| `NoCilium` | `network mode "<m>" needs Cilium (CiliumNetworkPolicy CRD not found); this cluster can only run full` (enforcement `auto`); `network enforcement is cilium, but the CiliumNetworkPolicy CRD was not found` (enforcement `cilium`, every mode) |
| `UnknownBundle` | `unknown network bundle "@<name>" (defined: @apt, @github, @node, @python)` |
| `InvalidAllowlist` | `invalid network host "<h>": must be a lowercase RFC 1123 hostname, optionally starting with "*."`, or `network allowlist has <n> hosts after bundle expansion, at most 20 are allowed` |
| `UnknownModelHost` | `no model API host is known for agent "<a>" with model "<m>", so it cannot run with network none or allowlist`, or `llm-keys openai-base-url "<v>" has no host` (Codex, OpenCode `openai/...`, or an `openai` judge without `baseURL`) |

Before the network is resolved, a run whose agent the SkillCIConfig judge cannot score (see "LLM keys", "Agent and judge") ends `Failed` with `judge provider "<p>" cannot score agent "<a>": the runner supports the judge providers <list> for it (SkillCIConfig spec.judge)` and no Job; it gets no `NetworkPolicy` condition.

A run that starts gets `NetworkPolicy` `True`, reason `Enforced` (a policy enforces the mode), or `False`, reason `NotEnforced` (no Cilium with `auto` and mode `full`, or SkillCIConfig `network.enforcement: off`), and `status.network`. With `auto` and no Cilium only `full` runs (a warm pool template is `Managed`: Agent Sandbox's internet-not-cluster default). SkillEvals made before network modes have no `spec.network` once they are past `Pending`; they ran with `full` and no policy, and the reporting site says so.

**Policies** (`cilium.io/v2` `CiliumNetworkPolicy`, only when Cilium enforces):

- **Baseline**, one per namespace, created at controller start and on every SandboxProfile reconcile: `skillci-trial-baseline`, `endpointSelector` `skillci.dev/component: trial-sandbox` (every warm or claimed trial pod of a profile), egress DNS only. A warm pod that is not claimed yet, or a trial pod without an eval policy, has no egress (fail closed). Kubernetes NetworkPolicies and CiliumNetworkPolicies selecting a pod add up, so the eval policy opens what its mode allows.
- **Per SkillEval**, created or updated before its Job, owned by the SkillEval: `skillci-net-<SkillEval name>` (over 63 characters: the first 52, `-` and 10 hex characters of the SHA-256 of the full name), labels `app.kubernetes.io/managed-by: skill-ci`, `skillci.dev/eval: <name>`, `endpointSelector` `{sandbox.users.io/skillci-eval: <name>, sandbox.users.io/skillci-role: trial}`. Egress: DNS to `kube-system/kube-dns` (`k8s-app: kube-dns`) on port 53 `ANY` with `rules.dns: [{matchPattern: "*"}]`; `full` adds `toEntities: [world]`; `none` and `allowlist` add `toFQDNs` (`matchName`, `*.x` as `matchPattern`) on TCP 443.

Trial pod labels use the `sandbox.users.io` domain because Agent Sandbox lets a SandboxClaim put only that domain on a claimed pod (`additionalPodMetadata.labels`); the bare Sandbox path puts them into the Sandbox pod template. Next to `skillci-eval` and `skillci-role`, every trial pod carries `sandbox.users.io/skillci-trial=<trial label>`: the Harbor trial name (`rn-basic__sryqofs`, the `session_id` without `__env`) when it is a valid label value of at most 63 characters, otherwise its first 52 characters, `-` and the first 10 hex characters of its SHA-256. No policy selects it; the runner uses it to map Hubble flows to trials.

## Eval reports

After scoring, the runner attaches the SkillEvaluator output to the evaluated skill digest as an OCI referrer, so it outlives the pod:

```text
oras attach --artifact-type application/vnd.skillci.report.v1+json \
  --annotation skillci.dev/model=<model> --annotation skillci.dev/agent=<agent> --annotation skillci.dev/agent-version=<version> \
  --annotation skillci.dev/lift=<+0.59> \
  --annotation skillci.dev/trials=<bundle digest> \
  <SKILL_REF> report.html:text/html result.json:application/json [BENCHMARK.md:text/markdown]
```

`SKILL_REF` is pinned to the digest, so the subject is exactly the version that was scored. Re-running an eval adds another referrer. `make report SKILL=<skill> VERSION=<vX.Y.Z> [MODEL=<model>] OUT=reports/` lists the referrers (`oras discover`) and downloads the newest report per agent@version/model entry.

The report is attached after the trial bundle (see "Trial bundles") and names it in `skillci.dev/trials`, or says why there is none in `skillci.dev/trials-withheld`. Its files go through the bundle's redaction and fail-closed scan first. When Tier 3 started and the run then failed, the runner attaches the report anyway (best effort) and reports its digest next to `error`, so the `Failed` run keeps its report and trials.

## Trial bundles

After Tier 3 the runner attaches every trial of the run, redacted, to the evaluated skill digest as a second OCI referrer, before the report (see "Eval reports"). Failed runs are bundled too, whenever Tier 3 left a run directory. Both referrers are accessories of the skill digest, so Harbor deletes them with the version; `make report` filters on the report's artifact type and ignores bundles.

```text
oras attach --artifact-type application/vnd.skillci.trials.v1+json \
  --annotation skillci.dev/eval=<SKILLEVAL_NAME> --annotation skillci.dev/agent=<agent> \
  --annotation skillci.dev/agent-version=<version> --annotation skillci.dev/model=<model> \
  --annotation skillci.dev/trials-schema=1 --annotation skillci.dev/redaction=v1 \
  <SKILL_REF> index.json:application/vnd.skillci.trials.index.v1+json \
  trial-<trial id>.json.gz:application/vnd.skillci.trial.v1+gzip ...
```

| Layer (title) | Media type | Content |
| --- | --- | --- |
| `index.json` | `application/vnd.skillci.trials.index.v1+json` | The run: skill, version, digest, SkillEval, entry, the eval cases, one entry per trial with the numbers the trial grid shows, the withheld trials, the credential routes by name (role `agent` or `judge`, the variable SkillEvaluator reads, for the judge `ANTHROPIC_API_KEY` under `anthropic`, `SKILL_EVAL_LLM_API_KEY` under `openai-compatible`, and the host), the caps, the network state. Schema: `docs/schemas/skillci.trials.index.v1.json` |
| `trial-<trial id>.json.gz` | `application/vnd.skillci.trial.v1+gzip` | One trial: meta (timing phases, agent, exception), prompt, the ATIF trajectory, `reward.json`, `failure.json`, the agent log tail, truncations, redaction counts, network. gzip of compact UTF-8 JSON. Schema: `docs/schemas/skillci.trial.v1.json` |

Layer names are flat, because oras rejects absolute paths. A trial id matches `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`. A trial directory whose name does not is left out of the index altogether (a withheld entry needs a valid id too) and the runner logs it; a second trial with an id already seen is withheld (`unreadable`). `testdata/trials/` holds golden bundles (a real DOKS run, a failed one, synthetic codex and opencode trials) with the input they are built from.

**Sources.** Only SkillEvaluator's redacted per-trial copies, `<run_dir>/<agent>/<with-skill|without-skill>/trials/<trial id>/` (`trajectory.json`, `reward.json`, `result.json`, `failure.json`, `exception.txt`, `artifact_manifest.json` and the agent log `claude-code.txt`, `codex.txt` or `opencode.txt`), the run's `result.json` (case and attempt of each trial from `agents.<agent>.pass_at_k.<arm>.cases.<case>.attempts[]`, else `trial_failures`) and `dataset_snapshot.json` (the cases). Never `_harbor-jobs` (it holds the agent's `CLAUDE_CONFIG_DIR` with user and machine ids), `config.json` (kubeconfig path, registry, namespace), `trial.log` or `lock.json`. The runner does not pass `--harbor-keep-jobs`.

**Trial document.** `meta` is a subset of Harbor's trial `result.json`: task name, start and finish, the phases that ran, agent name, version, model and provider, the agent result totals, and the exception with the last 8 KiB of `exception.txt` as traceback. `prompt` is the first `source=user` step, or the case question when there is no trajectory. `trajectory` is ATIF exactly as SkillEvaluator wrote it, except that strings over the cap are shortened in place, so step indices and JSON pointers stay valid; `trajectoryOmitted` says why there is none. `reward` and `failure` are the files as written. Evidence pointers inside `reward` stay relative to the trajectory and count steps from 0 (`/steps/3` is ATIF `step_id` 4); `truncations[].pointer` is relative to the trial document. `agentLog` holds the last 64 KiB of the agent log as one string (`text`, `bytes`, `truncated`); the one-string cap does not apply to it. `status` is `scored` (a reward exists), `unscored` (no reward: the trial failed before or during scoring) or `error` (the runner could not read the trial; only `meta` is kept, and its index entry has error type `Unreadable` with the reason as message). A trial whose files raise an error while it is read or fitted is withheld (`unreadable`).

**Index entry per trial**: id, layer, arm (`with-skill` or `without-skill`), case, attempt, status, score and passed (scored trials; passed is score at or above the arm's `pass_threshold` in the run's `result.json`, `agents.<agent>.pass_at_k.<arm>.pass_threshold`, default 0.5), error type and the first 300 characters of the message (unscored trials), start, duration and agent execution time (seconds, one decimal), steps, tool calls, the `final_metrics` tokens (in, out, cached), the total cost with its source (`estimate` when ATIF says `litellm_estimate`, else `reported`; per-step costs are always estimates), expected behaviors passed of total, `skill_invoked`, compressed and raw size, `truncated` (true when a string was shortened, the agent log was cut to its tail, or a step of the trial layer cap applied), and the flow counts.

**Network section.** When the Job has `HUBBLE_SERVER` and `TRIAL_BUNDLE` is not `off`, the runner first asks the relay for its status (`hubble status`, 10 s), then runs `hubble observe --server $HUBBLE_SERVER --timeout 10s --follow --output jsonpb --label sandbox.users.io/skillci-eval=<SKILLEVAL_NAME>` for the length of Tier 3 (hubble CLI v1.20.2, pinned by version and SHA-256 in `runner/Dockerfile`; `--timeout` is the dial timeout) and maps each flow to its trial by the source pod's `sandbox.users.io/skillci-trial` label (for DNS answers and other replies, the destination pod's). Each trial document gets `network`: the DNS names asked (count, first and last time, rcode), and the connections grouped by verdict (`FORWARDED`, `DROPPED`, `ERROR`, `AUDIT`), destination (the DNS name Hubble knew, else the IP), port and protocol, with count, first and last time, the drop reason and the first IP seen for a DNS name. A count is the number of distinct connections, so retransmits and the several events Cilium reports per connection count once. Two kinds of noise are left out: a DNS name that failed and is a resolver search-path expansion of another name the trial asked for (`pypi.org.<ns>.svc.cluster.local` next to `pypi.org`), and connections to multicast, link-local or unspecified addresses (IPv6 neighbor discovery to `ff02::2` and the like), which the pod's own network stack sends. A trial with no flow of its own gets an empty section with the run's state. The index has the run-level state: `collected`, `partial` (Hubble reported lost events or unavailable nodes, the CLI died after it delivered flows, or a row cap applied), `unavailable` (the relay did not answer within 10 s, or the CLI failed before any flow arrived; the reason says which) or `off` (no `HUBBLE_SERVER`, no collector; the index says why and the trial documents have no `network`). Flows never fail a run. Hubble's ring buffer can drop flows on a busy node, so a section can be partial.

**Redaction v1, before anything is uploaded.** The runner parses every JSON file and walks every string value, so JSON escaping cannot hide a value; text files (the agent log, the traceback) are one string each. Keys are never changed, so pointers stay valid. In order:

1. `exact`: the values (8 characters or more) of `SECRET_ENV` (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `NVIDIA_API_KEY`, `OLLAMA_API_KEY`, `OPENROUTER_API_KEY`), `SKILL_EVAL_EMBEDDING_API_KEY`, `SKILL_EVAL_LLM_API_KEY`, `AWS_BEARER_TOKEN_BEDROCK`, every other variable whose name ends in `_API_KEY`, every `HARBOR_*` variable whose name contains `KEY`, `TOKEN`, `SECRET`, `PASSWORD`, `PASSWD`, `AUTH` or `CREDENTIAL`, and the `auth`, `password`, `identitytoken` and `registrytoken` values of `/auth/config.json` with the password inside each `auth`. Each value also in standard base64 (with and without padding), URL-safe base64, URL-encoded and JSON-escaped form. They become `[redacted:<NAME>]`.
2. `pattern`: SkillEvaluator's `redact_sensitive_text` (the `sk-`, `nvapi-`, Bearer, JWT, GitHub, AWS and other shapes it knows, and sensitive `key=value` assignments, with its own markers such as `<redacted>`), plus Ollama-style keys (32 hex characters, a dot, 24 or more URL-safe base64 characters), which become `[redacted]`.
3. `envDump`: the value of every `NAME=value` line whose name contains `KEY`, `TOKEN`, `SECRET`, `PASSWORD`, `PASSWD`, `AUTH` or `CREDENTIAL` (any case, optional `export`) becomes `[redacted]`, unless an earlier rule redacted it already. The pattern rule catches most such lines first, so they end as `NAME=<redacted>`.

`redaction` in each trial document counts the string values each rule changed, never the values. Pattern redaction also over-redacts: SkillEvaluator already writes some token counts as `<redacted>`, and the site shows them as unknown.

**Fail closed.** After redaction and shortening the runner serializes each trial document and scans the bytes, before gzip, for every form of every exact value. A hit withholds that trial (`withheld[]` in the index, reason `secret-scan`) and logs the name of the secret, never its value. A hit in the index withholds the whole bundle. The same redaction and scan apply to the report files before they are attached; a report file that still leaks is not attached, and when the redaction cannot run at all (SkillEvaluator's `redact_sensitive_text` is not importable, or it fails) no report file is attached and no report is made. The controller redacts again when it renders a session (defense in depth: the same patterns plus the exact values of the `llm-keys` Secret). What redaction cannot catch: a secret the agent fetched from somewhere else, a partial key, or a key the agent rewrote. `testdata/trials-secrets/` is a canary trial with fake keys in every form.

**Caps** (the index records them in `limits`):

| Cap | Default | Past it |
| --- | --- | --- |
| One string | 32 KiB | The first 24 KiB and the last 8 KiB are kept with `...[skill-ci: <n> bytes cut]...` between them, and `truncations` gets an entry |
| One trial layer | 1 MiB compressed | The agent log tail is dropped, then every observation is cut to `TRIAL_OUTPUT_MAX_BYTES` (8 KiB, at least 256), then the trajectory is left out (`trajectoryOmitted`); still over: the trial is withheld (`size`) |
| The whole bundle | `TRIAL_BUNDLE_MAX_BYTES`, 24 MiB compressed (at least 1024) | Trials are withheld (`size`): the highest attempt first, then the later case, then the `without-skill` arm. An index alone over the limit gives no bundle (`size`). Every layer stays under the controller's 16 MiB blob limit |
| Trials per arm | 50, as `MAX_SAMPLES` | The rest are withheld (`limit`) |
| Agent log | 64 KiB | Only the last 64 KiB |
| Traceback | 8 KiB | Only the last 8 KiB of `exception.txt` |
| Flow and DNS rows | 200 each per trial | `truncated: true`, network state `partial` |

A run of 18 trials stores about 0.5 to 1 MB. A `TRIAL_BUNDLE_MAX_BYTES` or `TRIAL_OUTPUT_MAX_BYTES` that is not an integer, or is below its minimum (the index schema's), is ignored and logged, and the default applies.

**Pairing with the report.** The bundle is attached first, then the report with `--annotation skillci.dev/trials=<bundle digest>`, or with `--annotation skillci.dev/trials-withheld=<reason>` when there is no bundle: `disabled` (`TRIAL_BUNDLE=off`), `no-trials` (Tier 3 left no trial directory or no run directory, and every Rejected run, whose Tier 3 never ran), `secret-scan` (the index leaked), `size`, `build-failed` (the builder is missing, the redaction cannot run, or the run cannot be described: no digest in `SKILL_REF`, an unknown agent, an empty `SKILLEVAL_NAME`) or `attach-failed`. A Rejected run and a run whose Tier 3 left no run directory still get their report, with `trials-withheld=no-trials` (or `disabled`). The controller finds the bundle through the report manifest it already reads, so no CRD field holds it. A bundle that cannot be built or attached never fails the eval, the same rule as for the report. A retried run keeps the reports, and so the bundles, of its earlier Jobs in `status.attempts`.

**Error path.** When Tier 3 started and the run then fails, the runner still builds the bundle, attaches it and the report on a best-effort basis, and puts `report` into the error payload (see "Runner Job contract"). Without a run directory the report is attached with `trials-withheld=no-trials`. An error before Tier 3 started (a missing dataset, a failure of the Tier 1 + 2 run itself) attaches nothing. A failed run, when the session matters most, keeps its trials.

## All tiers and publication

Every run evaluates the skill version through all three SkillEvaluator tiers, Tier 1 first as a cheap gate:

1. **Tier 1** (static, security, quality: schema, security, pii, license, code-integrity, unicode, version, quality, lint) and **Tier 2** (deduplication against the other skills in Harbor) run before any live trial.
2. If Tier 1 or Tier 2 fails, the runner does **not** run Tier 3: no sandboxes, no tokens. It still exits 0 and reports the result.
3. Otherwise **Tier 3** (live with/without-skill evaluation, as before) runs, and SkillEvaluator's publication assessment covers all three tiers.

The termination message gains these fields (scores stay as before; they are absent when Tier 3 was skipped):

```json
{"withSkill": 0.95, "withoutSkill": 0.36, "lift": 0.59, "agent": "claude-code", "model": "claude-haiku-5-5",
 "report": "sha256:...",
 "tiers": {"tier1": "passed", "tier2": "passed", "tier3": "passed"},
 "publication": "passed",
 "findings": ["tier1/security: hardcoded credential in scripts/deploy.sh"]}
```

- Tier values: `passed`, `failed`, `incomplete` (a check could not produce trustworthy evidence, e.g. a scanner missing), `skipped`.
- `publication` is SkillEvaluator's publication assessment: `passed`, `failed`, `incomplete`.
- `findings`: at most 5 short strings naming the failing tier/check and why; empty when everything passed. Keep the whole message under 4 KB.
- Reports from all tiers are attached to the skill digest as before (one referrer per run).

SkillEval status gains `tiers` (`tier1`, `tier2`, `tier3` strings), `publication` and `findings` (list). New verdict **`Rejected`**: Tier 1 or Tier 2 failed. A Rejected run never counts toward promotion; a digest with a Rejected run on any required model never gets `beta` or `stable`, and loses `stable` if it had it. Gauge `skillci_publication{skill,version,model,status}` is 1 for the run's publication status.

## Triggers

1. **Skill push.** Harbor webhook policy on project `skills`, events `PUSH_ARTIFACT`, target `http://skill-ci-controller.skill-ci.svc.cluster.local:8080/webhook/harbor`, payload format `Default`. The controller acts only when the pushed tag matches `^v\d+\.\d+\.\d+$`. Other tags (`beta`, `stable`) are ignored. For each entry in the list it creates a SkillEval unless one already exists for the same digest, agent, agent version and model (dedupe by labels), with the payload's `operator` (the Harbor user who pushed) as annotation `skillci.dev/pushed-by` when present.
2. **Model list change.** The controller watches ConfigMap `skill-ci-models`. For each added entry (a new agent, a new agent version of an agent already listed, or a new model: the entry identity is agent + agentVersion + model) it creates SkillEvals for every skill digest currently tagged `beta` or `stable` in Harbor (`trigger: model-change`). For an entry whose `required` flag changed it updates `spec.required` of the entry's SkillEvals.

## Statistics

One run is 3 eval cases x `attempts` trials per arm, so up to 9 trial scores per arm. The same skill version scored 0.95 on one run and 0.87 on the next, so a fixed number is a poor gate. The gate asks for evidence: a confidence interval at level `--confidence` (default 0.95, so `alpha = 0.05`).

For two samples `a` and `b` (at least two scores each) the controller computes, in `internal/stats`:

```text
mean(x)      = sum(x) / n
s2(x)        = sum((x - mean)^2) / (n - 1)            sample variance
stderr(x)    = sqrt(s2(x) / n)                        standard error of the mean
va, vb       = s2(a) / n_a,  s2(b) / n_b
diff         = mean(a) - mean(b)
nu           = (va + vb)^2 / (va^2 / (n_a - 1) + vb^2 / (n_b - 1))      Welch-Satterthwaite degrees of freedom
CI(a, b)     = diff +/- t(1 - alpha/2, nu) * sqrt(va + vb)              Welch's t interval
```

`t(p, nu)` is the quantile of Student's t distribution (inverse of its distribution function by the regularized incomplete beta function; unit tests check it against SciPy and t tables). If both samples have no spread the interval collapses to `diff`. With fewer than two scores in an arm there is no interval and the fixed-number rules apply.

Two intervals are used, both Welch's, both on the two-decimal trial scores (trials are treated as independent draws; the cases differ in difficulty, which only widens the spread):

- **Lift interval**: `a` = with-skill scores, `b` = without-skill scores of one run. Stored as `status.liftCI`.
- **Version interval**: `a` = with-skill scores of the candidate, `b` = with-skill scores of the stable digest, per required model.

Example (9 trials per arm): with-skill `0.95` x 9 against without-skill `0.36 0.38 0.35 0.30 0.40 0.35 0.36 0.37 0.38`: lift `+0.59`, 95% interval `[+0.57, +0.61]`.

## Policy

The policy works per model list entry, `agent@version/model` (for example `claude-code@2.1.292/claude-opus-5-5`; `agent/model` for an entry without a version). Results of the same model under another agent or another agent version are other entries: they are never compared with each other, and each required one has to pass. A run is held against the stable version only on the entry it ran on.

Per SkillEval verdict (SkillEvaluator bands, plus the interval when the run has one): `lift >= +0.05` **and** the lift interval's lower bound `> 0` is Helps, `lift <= -0.10` **and** the interval's upper bound `< 0` is Hurts, otherwise Neutral; Neutral with `withoutSkill >= 0.80` is Obsolete. A run without trial scores uses the lift bands alone. `lift` is the runner's two-decimal lift. The version-over-version gate below overrides the verdict with `Regressed` or `Inconclusive` on the models that scored worse than the stable version; the policy itself always reads the no-skill bands, never the stored verdict.

Per digest, once every required model has a Succeeded SkillEval:

- Tag the digest `beta` when it is the newest evaluated version (controller manages `beta`).
- **Promote**: on all required models `lift >= --promote-threshold` (default `+0.10`) **and** the lift interval's lower bound `> 0` (the skill helps beyond noise), and no regression against the current stable version (below): add tag `stable` (move it from any other digest of that skill). A model whose lift reaches the threshold but whose interval includes 0 blocks promotion; the `PolicyApplied` message names it ("lift not beyond noise on claude-code@2.1.292/claude-opus-5-5 (+0.12, 95% CI [-0.05, +0.29])").
- **Regression gate**: if another digest of the skill is tagged `stable`, compare the candidate's with-skill score with that digest's, per model. A model counts when it is required and both digests have a Succeeded SkillEval for it. The difference is `candidate - stable`, the mean of the with-skill trial scores of each side (the runs' with-skill scores when either side has no trial scores). If `difference < -tolerance` (`--regression-tolerance`, default `0.05`, compared at two decimals, a drop of exactly the tolerance passes), the version interval decides:
  - interval's upper bound `< 0`: **Regressed**. The digest is not promoted and the current stable stays. Its SkillEvals get condition `Regressed` (reason `WorseThanStable`, message names the model, both versions, both scores, the difference and the interval), a Warning event, and verdict `Regressed` on the models that dropped.
  - interval includes 0 (upper bound `>= 0`): **Inconclusive**. The digest is not promoted and the current stable stays, exactly as for Regressed, but the evidence does not say the drop is real. Its SkillEvals get condition `Inconclusive` (reason `NotEnoughEvidence`) with the numbers, for example `claude-code/claude-sonnet-5-5: 0.87 with v1.2.0 vs 0.95 with stable v1.1.0, difference -0.08, 95% CI [-0.18, +0.03]; not enough evidence to call it a regression`, a Normal event, and verdict `Inconclusive` on the models that dropped. A later run with more trials, or a rerun, can settle it either way.
  - either side has no trial scores (older runners): the fixed rule decides, a drop beyond the tolerance is Regressed, and the message ends with `fixed rule, no trial scores to test it`.

  The candidate still gets `beta` in all three cases. Optional models never gate. With no current stable version, or when the candidate is the stable digest, nothing is compared. When both Regressed and Inconclusive occur (different models) the digest carries both conditions.
- **Newest wins ties**: an older version never takes `stable` from a newer stable version unless that newer version scores worse than it, beyond noise (a Regressed comparison the other way round; an Inconclusive one does not count). Without this rule two equally good versions would swap the tag on every reconcile.
- **Demote**: `lift <= --demote-threshold` (default `-0.10`) and the lift interval's upper bound `< 0` on any required model: remove tag `stable` from that digest. Without trial scores the lift alone decides.
- **Obsolete**: verdict Obsolete on all required models: flag only (metric + condition), never delete.
- **No trial scores**: a run whose termination message had no `samples` (runner before 0.5.1) uses the fixed-number rules for its own lift, and the `PolicyApplied` message says `fixed thresholds, no trial scores from <model>`.
- **Rejected**: a run whose Tier 1 or Tier 2 failed ends `Succeeded` with verdict `Rejected` and usually no scores (Tier 3 was skipped; a Succeeded run without scores and without a failed tier is marked `Failed` instead). It counts as finished for "every required model has a result". If any required model is Rejected, the digest gets no `beta` and no `stable`, loses `stable` if it holds it, and is never the "newest evaluated version" that takes `beta` (the older version keeps it). The Rejected run gets condition `Rejected` (reason `Tier1Failed` or `Tier2Failed`, message the findings) and a Warning event with the same reason. Rejected runs are left out of the regression comparison. Rejected optional models never gate. A termination message from a runner without `tiers` still works: the tiers are unknown, nothing is Rejected.

Worked example (both required models, stable `v1.0.0` with-skill 0.95 on 9 trials):

| Candidate with-skill trials | Mean | Difference | 95% interval | Result |
| --- | --- | --- | --- | --- |
| `0.70 0.71 0.69 0.70 0.70 0.71 0.69 0.70 0.70` | 0.70 | -0.25 | `[-0.26, -0.24]` | Regressed |
| `0.95 0.55 0.90 0.85 1.00 0.80 0.90 0.95 0.95` (stable spread too) | 0.87 | -0.08 | `[-0.18, +0.03]` | Inconclusive |
| same scores as stable | 0.95 | 0.00 | `[-0.01, +0.01]` | passes |
| `0.99 0.98 1.00 0.99 0.99 0.98 1.00 0.99 0.99` | 0.99 | +0.04 | `[+0.03, +0.05]` | passes |

Verdicts depend on other digests (the stable one), so when a SkillEval reaches `Succeeded`, or the policy changes a tag, the controller re-enqueues the other `Succeeded` SkillEvals of the same skill (all digests). They re-run the policy and converge; a re-run that changes nothing writes nothing and enqueues nothing.

Tag changes go through the Harbor REST API (`POST /api/v2.0/projects/skills/repositories/<skill>/artifacts/<digest>/tags`, `DELETE .../artifacts/<digest>/tags/<tag>`). Unlike `oras tag`, these do not fire `PUSH_ARTIFACT`. Thresholds are controller flags (`--helps-threshold`, `--hurts-threshold`, `--promote-threshold`, `--demote-threshold`, `--obsolete-baseline`, `--regression-tolerance`, `--confidence`). `--confidence` is the level of both intervals (default `0.95`, exclusive between 0 and 1); a higher level asks for more evidence to promote and to call a regression. Helm: `controller.thresholds.confidence`.

## Metrics (controller `:8081/metrics`)

Gauges labeled `skill`, `version`, `agent`, `agent_version`, `model` (`agent_version` is the pinned release, else the one the runner reported, else `unknown`; two entries differing in agent, agent version or model are two series): `skillci_lift`, `skillci_with_score`, `skillci_without_score`, and, for runs with trial scores, `skillci_lift_ci_lower` and `skillci_lift_ci_upper` (the lift interval at `--confidence`; absent for runs without trial scores). Gauge `skillci_stable{skill,version}` (1 when tagged stable). Gauge `skillci_obsolete{skill,version}`. Gauge `skillci_regressed{skill,version}` (1 when the version scores worse than the stable version beyond the tolerance and beyond noise). Gauge `skillci_inconclusive{skill,version}` (1 when it scores worse beyond the tolerance but the interval includes 0, and no model regressed beyond noise). Gauge `skillci_publication{skill,version,agent,agent_version,model,status}` is 1 for the publication status of the run (`passed`, `failed` or `incomplete`; one series per skill, version and entry, replaced when a rerun changes the status, absent for runs without a publication status). Gauge `skillci_rejected{skill,version}` is 1 when a required model's run failed Tier 1 or Tier 2. Counter `skillci_skillevals_total{phase}` counts phase transitions. Counter `skillci_retries_total{reason}` counts runs started again after an infrastructure failure (`reason` is `env_start_timeout`, `connection_lost`, `agent_install`, `agent_setup`, `job_lost` or `rate_limit`; all six series exist at 0 from startup). Gauge `skillci_skillevals{phase}` is the current number of SkillEvals per phase (Pending, Running, Succeeded, Failed, Planned; recounted from the cache every 5 s, zero when none). When a SkillEval is deleted the controller removes its `skillci_lift`, `skillci_lift_ci_lower`, `skillci_lift_ci_upper`, `skillci_with_score` and `skillci_without_score` series, and the `skillci_publication` series of its entry, and the `skillci_stable`, `skillci_obsolete`, `skillci_regressed`, `skillci_inconclusive` and `skillci_rejected` series of its version once no SkillEval of that skill and version remains, so dashboards never show the numbers of a deleted run.

## OpenTelemetry export (optional, OTLP)

The controller can push the same metrics over OTLP, next to the scrape endpoint, for any OpenTelemetry backend: an OpenTelemetry Collector, Grafana Alloy, the Prometheus OTLP receiver or a vendor endpoint. It is off unless an endpoint is set. Settings follow the pulumi-exporter (`pulumi-labs/pulumi-exporter`); each flag defaults to its environment variable:

| Flag | Environment variable | Default | Meaning |
| --- | --- | --- | --- |
| `--otlp-endpoint` | `OTEL_EXPORTER_OTLP_ENDPOINT` | empty (off) | `host:port`, or a URL: `http://` turns TLS off, a URL path is the base `/v1/metrics` is appended to (as the OpenTelemetry spec defines for this variable) |
| `--otlp-protocol` | `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf` | `http/protobuf` or `grpc` |
| `--otlp-url-path` | `OTEL_EXPORTER_OTLP_METRICS_URL_PATH` | `/v1/metrics` | Full metrics path for `http/protobuf`; `/api/v1/otlp/v1/metrics` for the Prometheus OTLP receiver. Wins over a URL path in the endpoint |
| `--otlp-insecure` | `OTEL_EXPORTER_OTLP_INSECURE` | `false` | No TLS |
| `--otlp-headers` | `OTEL_EXPORTER_OTLP_HEADERS` | empty | `key=value` pairs, comma separated, values may be URL encoded (an ingest or API key) |
| `--otlp-interval` | `OTEL_METRIC_EXPORT_INTERVAL` (milliseconds, read by the SDK) | 60s | Time between exports |

What is exported: the `skillci_*` families above and nothing else (not the Go runtime or controller-runtime series of `/metrics`), with the same names, labels and values. A Prometheus bridge (`go.opentelemetry.io/contrib/bridges/prometheus`) reads the collectors at every export, so there is one source of truth. Gauges stay gauges, `skillci_skillevals_total` and `skillci_retries_total` are cumulative monotonic sums. Resource: `service.name=skill-ci-controller` (`OTEL_SERVICE_NAME` overrides it), `service.version` (the controller release), the SDK attributes, and `OTEL_RESOURCE_ATTRIBUTES`; the Helm chart sets `k8s.pod.name` and `k8s.namespace.name` there. Every replica exports (no leader election), as every replica serves `/metrics`. Export errors are logged (logger `otlp`) and never stop the controller; on shutdown the exporter flushes once (at most 10 s).

Do not scrape and push into the same Prometheus: it would store every series twice (the pushed copies carry `job=skill-ci-controller`, from `service.name`), and the dashboard queries would sum both. Either keep the ServiceMonitor or point the OTLP export at Prometheus, not both.

## Reporting site (controller `:8082`)

Read-only. A single page app (React, built from `ui/` into `controller/internal/reportsite/web/dist/` and embedded with `go:embed`) on a JSON API under `/api/v1/`, both served by the controller. Data: SkillEvals from the manager's cache, channel tags from the Harbor REST API (cached 5 s), the model list ConfigMap, and reports and trial bundles from the OCI referrers named by `status.reportDigest` and `status.attempts[].reportDigest` (fetched with the registry API using `HARBOR_USERNAME`/`HARBOR_PASSWORD`, cached in memory by digest: 32 MiB for reports, a separate budget for trial documents). A run without `reportDigest` shows "not stored". No external resources: no CDN, no web fonts, system font stacks.

**Pages.** The controller answers every GET path that is not an API, asset or report file route with the app's `index.html`, and the app's router renders the page (history routing):

| Route | Content | API |
| --- | --- | --- |
| `/` | Overview: counts (runs by phase: Pending, Running, Succeeded, Failed, and Planned when there are any, adding up to Runs; skills, on stable, held back and rejected versions; each tile links to its list), needs attention (failed runs, held back and rejected versions, running runs, pending runs, each with the pill of its phase), the 20 newest runs, release channels per skill (Stable, Beta, Held back, Rejected), and the network policy in force (default mode, allowed modes, bundles, enforcement, the Hubble Relay, and whether it comes from the SkillCIConfig `default`, its last valid spec or the built-in defaults; a notice when the SkillCIConfig is invalid) | `overview` |
| `/runs` | All runs, newest first, with Agent (`agent@version`), Model, Tiers (T1 T2 T3 markers), Publication and Trials columns. Trials is Sessions (a link to the grid), Withheld with the reason in a few words, or None with why: not yet (the run is active), no report, Tier 3 not run, bundles off, no trials, or older run (a report from before trial bundles). Filters: `skill`, `agent` (`agent@version`), `model`, `verdict`, `phase`, `publication`, `trigger`, `q` (part of the name); sort; pages of 50 | `runs` |
| `/runs/{name}` | One SkillEval (title with model and `agent@version`, an Agent version row that says when the trials ran another release than the spec pins): scores, the SkillEvaluator tiers with findings (also for a `Failed` run whose error came after the gate), the sandbox (profile and warm pool, or a bare Sandbox), the network (mode and its source, what the trials could reach, the declared entries, the allowed hosts, the enforcing CiliumNetworkPolicy or why none, the `NetworkPolicy` message), conditions, the six SkillEvaluator dimensions when a report is stored. A run without scores (Tier 3 skipped) shows "Tier 3 not run: Tier 1 rejected this version, so no live trials were spent." and no score or dimension blocks. A run with `status.retries > 0` shows "Retried once after an infrastructure failure: <message of the Retried condition>" ("Retried N times ..." for more) next to the status message. Plus the run's attempts (`?job=` shows an earlier one) and the trial grid: cases x attempts x arm with score and status, links to the sessions, a Compare link per case, totals (tokens, cost, trial time, unscored and withheld trials) and the credential routes by name. Without a bundle the grid comes from `result.json`: scores, no links, and one line that says why there are no sessions (the run is older than trial bundles, bundles were off, or the runner withheld the bundle and why) | `runs/{name}` |
| `/runs/{name}/trials/{trial}` | Session, Log tab: the prompt, the metadata strip (arm, case and attempt, agent and model, workspace, skill use, credential routes, times and phases, tokens, cost, criteria passed, trigger, pushed by), the log (one entry per ATIF part, category chips with counts, Focus and All presets, jump links), the eval criteria and the session findings | `runs/{name}/trials/{trial}` |
| `/runs/{name}/trials/{trial}/files`, `/judge`, `/network` | The files the agent wrote or edited (from its tool calls), every judge metric with its details, the DNS names and flows | the same |
| `/runs/{name}/cases/{case}/compare` | With and without the skill side by side for one case; attempt k is paired with attempt k (`?attempt=`, or `?with=&without=` trial ids) | `compare` |
| `/skills` | Skill catalog: stable, beta and newest version, runs with the running and pending ones, last run, held back and rejected versions, required entries passing | `skills` |
| `/skills/{skill}` | Versions x entries (one column per `agent@version` and model: with, without, lift, verdict, tier markers), channel tags, policy decision per version ("Rejected by Tier 1: <first finding>") | `skills/{skill}` |
| `/models` | The model list entries (required, sandbox profile) with runs, verdicts, mean lift, failures and retries, and retired entries that still have runs | `models` |

Keyboard: `j` and `k` move between log entries, `/` focuses the filter, `g g` and `G` jump to the first and last entry. Status is never shown by color alone (an icon and a word go with it), focus is always visible, and the theme follows `prefers-color-scheme` with a light and dark toggle kept in `localStorage`.

**API.** `GET` and `HEAD` only; any other method gets 405. The response types are the Go types in `controller/internal/reportsite/apiv1`, mirrored by `ui/src/api/types.ts`. Fields are lowerCamelCase, lists are always present (`[]`), an unknown optional value is left out rather than `null`, times are RFC 3339 UTC.

| Route | Response | Status codes |
| --- | --- | --- |
| `/api/v1/site` | `Site`: namespace, project, controller release, thresholds | 200 |
| `/api/v1/overview` | `Overview` | 200, 503 |
| `/api/v1/runs?skill=&agent=&model=&verdict=&phase=&publication=&trigger=&q=&sort=&page=&pageSize=` | `RunList`; `sort` is `newest` (default), `oldest`, `skill` or `lift`; `pageSize` 1 to 200, default 50 | 200, 400, 503 |
| `/api/v1/runs/{name}?job=` | `RunDetail` | 200, 400, 404, 503 |
| `/api/v1/runs/{name}/trials/{trial}?job=` | `TrialSession` | 200, 400, 404, 502, 503 |
| `/api/v1/runs/{name}/trials/{trial}/steps/{step}/output/{k}?job=` | The full text of result `k` of the observation of ATIF step `step`, `text/plain; charset=utf-8` | 200, 400, 404, 502, 503 |
| `/api/v1/runs/{name}/trials/{trial}/trajectory.json?job=` | The redacted ATIF trajectory, `application/json`, as an attachment | 200, 400, 404, 502, 503 |
| `/api/v1/runs/{name}/cases/{case}/compare?attempt=&with=&without=&job=` | `Compare` | 200, 400, 404, 502, 503 |
| `/api/v1/skills` | `SkillList` | 200, 503 |
| `/api/v1/skills/{skill}` | `SkillDetail` | 200, 404, 503 |
| `/api/v1/models` | `ModelList` | 200, 503 |

Errors have the body `Error`, `{"error": {"status": 404, "code": "not_found", "message": "..."}}`, with the codes `bad_request` (400: a malformed parameter, or a trial id that does not match `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`), `not_found` (404: an unknown run, Job, skill, case, trial or route), `withheld` (404: the runner withheld the trial or the bundle), `internal` (500), `not_implemented` (501), `registry_error` (502: Harbor did not return a report or bundle file) and `unavailable` (503: the cluster could not be read). `job` must be one of the run's Jobs (`RunDetail.attempts`); a `{trial}` must appear in the bundle index of that Job, and registry paths are never built from raw input. A registry failure while building a `RunDetail` shows in its `report` and `trials` states, not as a status code. An unknown `/api/` path gets a JSON 404, never the app.

**Other routes.** `/runs/{name}/report` (the stored `report.html` as is, `text/html`), `/runs/{name}/result.json` (`application/json`, attachment) and `/healthz` (`ok`) are unchanged; the app links to them with full page loads. `/assets/{file}` serves the app's content-hashed files. A controller built without the UI serves a placeholder page that says to run `make ui-build`, with the API working as usual.

| Response | Cache-Control | Content-Security-Policy |
| --- | --- | --- |
| `index.html` (every page route) | `no-store` | `default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'` |
| `/assets/{file}` | `public, max-age=31536000, immutable` | the same |
| API: site, overview, lists, run detail | `no-store` | `default-src 'none'; frame-ancestors 'none'` |
| API: trial session, compare, output, trajectory | `private, max-age=300`, ETag of bundle digest, trial and query | the same; the output text adds `sandbox` |
| `/runs/{name}/report` | `private, max-age=3600` | its own: `connect-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'` |

Every response has `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`. The build emits no inline script or style; the app sets dynamic styles through the DOM, never through a `style` attribute in markup. Agent output is untrusted: the app renders it as text, never as HTML (no Markdown rendering, no auto-linking), and the controller redacts the session again when it builds it (see "Trial bundles"). The stored `report.html` loads Chart.js from a CDN inside its own page, which its CSP allows; on an airgapped cluster that page loses its charts and everything else works.

Lifts show their evidence wherever they appear (runs table, skill matrix cells, run page): `+0.59` with `95% CI +0.41 to +0.73, n=9/9` (the level is `--confidence`, `n` the trials with/without), left out for runs without trial scores. Verdict `Inconclusive` is neutral (hollow marker, text "Inconclusive"). The policy decision says in plain words when a version is "Not proven" (a drop that may be noise: the numbers, "The stable version stays; more trials would show whether the drop is real") or "Not promoted" because a lift is not beyond noise. The "How to read this" legend explains the interval in two sentences.

Policy decisions use the controller thresholds.

**Access.** The site has no authentication and no authorization. Whoever reaches port 8082 can read every run, report and trial session, and sessions hold agent output, skill content and eval prompts. Keep the `reports` port behind `kubectl port-forward` (`make reports-ui`) or an authenticating reverse proxy (for example oauth2-proxy in front of the Service); never expose it through an Ingress or a LoadBalancer without one. The site never shows a credential value: sessions name the route variables only.

**Serving.** The app is the only page mode; the controller has no server-rendered pages and no `--reports-ui` flag. A route takes only the query parameters of the table above, each at most once (anything else is a 400); `job`, `skill` and the run name must be valid object names, `sort`, `verdict`, `phase`, `publication` and `trigger` one of their values, and free-text filters at most 256 bytes without control characters. `/runs/{name}/report` and `/runs/{name}/result.json` take `?job=` too, for the report of an earlier attempt. Render-time redaction uses every value of `llm-keys` except `openai-base-url` (read with the API reader, so no Secret informer, and again after a minute), `HARBOR_PASSWORD` and `HARBOR_USERNAME:HARBOR_PASSWORD` (the registry auth value), each in every form of redaction v1, plus the v1 patterns. It covers every string of every response, not only sessions; a response that still holds a value is not sent (500, `internal`). When `llm-keys` cannot be read and never was, the session, compare, output and trajectory routes answer 503 and the other routes redact with the rest. The UI fixtures in `ui/src/api/fixtures/` are API responses written by the Go golden test `TestGoldenFixtures` (`controller/internal/reportsite`) from the bundles in `testdata/trials`: `go test ./internal/reportsite/... -update` rewrites them, and the test fails when one is out of date.

## Retry

A run that fails for an infrastructure reason is started again, because that failure says nothing about the skill. A run with a real result is never retried: retrying bad answers until they pass would bias the score. This mirrors Harbor 0.25's error classification (infrastructure errors retried, agent errors not).

- **Never retried**: a run with scores, a Tier 1 or Tier 2 failure (`Rejected`), a failed Job that also wrote a result, a runner `error` that comes with scores, and everything the classifier does not recognise. Unknown means not transient.
- **Transient** (package `internal/retry`, `Transient(message, jobFailedWithoutMessage)`), for a run that ends `Failed` without a result:
  - the Job failed and the runner left no termination message, or a pod of the Job was evicted/disrupted and left no readable result (`job_lost`);
  - the failure message names a Kubernetes-level loss: `DeadlineExceeded`, `Evicted`, `Preempt*`, `FailedScheduling`, `Unschedulable`, `ImagePullBackOff`, `ErrImagePull` (`job_lost`);
  - the runner error (the SkillEvaluator `execution_errors` of all trials, joined with a semicolon and a space after `no scores in result.json:`) consists only of transient parts. It is split at every `<Name>Error:` token and every part must qualify, so one agent error in the mix makes the run a real result:
    - `EnvironmentStartTimeoutError` (`env_start_timeout`);
    - `ApiRateLimitError` whose text is Harbor's `Command failed (exit N): <command> stdout: ...` and whose failed command does not mention a rate limit (`rate limit`, `too many requests`, any case) (`rate_limit`). Harbor raises it when the output of the failed command mentions a rate limit, and that output includes the agent's transcript, so a task about rate limits would make any failure of the agent look like one. The command must be visible up to `stdout:`, because it carries the task text;
    - any part containing `Connection to remote host was lost` (`connection_lost`);
    - `NonZeroAgentExitCodeError` whose failed command starts like the agent install (`apt-get`, `apk`, `yum`, `dnf`, `set -euo pipefail; if command -v apk`, `npm install`, `pip install`, `curl ... | sh`) (`agent_install`) or the agent setup (`mkdir -p $CLAUDE_CONFIG_DIR`) (`agent_setup`). Only the start of the command counts, because the agent's own task command carries the task text, which may mention anything.
  - The runner error must show every trial failure. It is not transient when the runner cut it (it ends with `...`), when SkillEvaluator notes that failure details were truncated, or when a trial failure (`Agent runtime failed in <trial>: ` or `Unscoreable reward in <trial>: `) does not start with an `<Name>Error:` token, since its text would otherwise count as part of the error before it. The other SkillEvaluator notes (`Harbor job did not complete successfully: N errored`, `Missing scored attempts for cases: ...`, `Scored attempt coverage is N/M`, `<agent> <arm> Harbor run failed: ...`) only summarise the trial failures and do not count.
  - `AgentTimeoutError` is the agent's own failure on its task and is never transient, nor is a `NonZeroAgentExitCodeError` of any other command.
- **Mechanics**: the budget is `--max-retries` (default 1, Helm `controller.maxRetries`; 0 disables). When a run would become `Failed`, the failure is transient and `status.retries < --max-retries`, the controller sets `status.retries` +1 and the condition `Retried` (`True`, reason `InfrastructureFailure`, message the failure of the previous Job), appends the previous Job to `status.attempts` (its name, its `reportDigest` when it attached a report, the failure message), clears `status.reportDigest`, writes phase `Pending`, emits a Normal event `InfrastructureFailure`, and only then deletes the old Job (propagation Background; the status write comes first so a lost race never reads a missing Job as a failure). The run then goes through the normal Pending path with a new Job named `<skilleval>-r<n>`: it waits for a slot (`--max-concurrent-jobs`) and spends its trials from `--trial-budget` again. The failed attempt keeps what it spent: the budget counts every Job started. When the budget is exhausted the retry stays `Pending` ("trial budget exhausted"). `completedAt` is set only when the run ends. The `Retried` condition stays after the run finishes; a second transient failure ends `Failed` with that failure as `status.message`.
- **Trial retries inside Harbor**: before a run can fail, Harbor itself runs a single trial again when it failed with `ApiRateLimitError`. The runner's `TRIAL_RETRIES` (default 2, `0` turns it off) reaches the patched SkillEvaluator (hunk 13), which passes Harbor `--max-retries <TRIAL_RETRIES> --retry-include ApiRateLimitError` and `-c` with a JobConfig file in the runner's work directory that holds only the backoff: 20 s before the first retry, doubling, at most 300 s. Harbor applies every other flag over that file, so it changes nothing else. A retry keeps the trial's name and replaces its result, so the scores, the trial bundle and the runner error show only the last try. The controller does not see these retries: `--trial-budget` counts attempts x 2 arms however often Harbor retried, and a retried trial only makes the run take longer. When the last try still fails, the rules above decide whether the run is retried. Harbor classifies a failure as `ApiRateLimitError` when the output of the failed command mentions a rate limit, and that output includes the task text, so a failed trial of a task about rate limits is retried at trial level too, at most `TRIAL_RETRIES` times. The run-level classifier above rules such commands out; Harbor's trial-level one cannot.

## Webhook auth

Secret `harbor-webhook` (key `token`). The Harbor webhook policy sets `auth_header` to `Bearer <token>`; the controller rejects requests whose `Authorization` header doesn't match (env `WEBHOOK_TOKEN` from that secret). If `WEBHOOK_TOKEN` is empty, the controller logs a warning and accepts all requests.

## Build and deploy

- `controller/Dockerfile`, build context `controller/`.
- `runner/Dockerfile`, build context repo root. Build arg `SKILL_CI_VERSION` sets the runner's default `SANDBOX_IMAGE` tag.
- `sandbox/Dockerfile`, build context `sandbox/`.
- `ui/`, the reporting UI: Node at the release in `ui/.nvmrc`, dependencies pinned exactly in `ui/package.json` and `ui/package-lock.json`, every version at least 14 days old when it was chosen (`ui/README.md`). `make ui-build` writes `controller/internal/reportsite/web/dist/` (generated, gitignored), which `go build` embeds; without it the controller embeds the placeholder `web/ui-placeholder.html`, so `go build` and `go test` need no Node. GoReleaser runs `make ui-install` and `make ui-build` in its before hooks, so released binaries and images always carry the UI. `make ui-test` runs eslint, `tsc --noEmit` and vitest; `make ui-dev` serves the UI on port 5173 against the fixtures in `ui/src/api/fixtures/`, or against a port-forwarded controller with `SKILLCI_API=http://localhost:8082`.
- Nothing is built in the cluster. A `vX.Y.Z` tag runs `.github/workflows/release.yaml`: GoReleaser publishes the controller image and binaries, the workflow builds and Cosign-signs the runner and sandbox images (with SBOM and provenance) and the Helm chart, all as `ghcr.io/dirien/...:X.Y.Z`. The chart's default image tags are its appVersion. `make deploy` installs the working-tree chart with the tags of the newest release (`TAG=...` overrides).
- All controller-side manifests (CRDs `SkillEval`, `SandboxProfile` and `SkillCIConfig`, controller Deployment and Service, RBAC, runner ServiceAccount and Role, the `default` SandboxProfile and the `default` SkillCIConfig) live under `config/` with a `config/kustomization.yaml`; deploy is `kubectl apply -k config/` (on a new cluster apply twice: the SandboxProfile and SkillCIConfig need their CRDs established first). The controller Role covers SkillEvals, SandboxProfiles (and status), SkillCIConfigs (read, and status), Jobs, pods, the model list ConfigMap, events, leases, `extensions.agents.x-k8s.io` SandboxTemplates and SandboxWarmPools (and warm pool status), `cilium.io` CiliumNetworkPolicies and `get` on the Secret `llm-keys` (for `openai-base-url`, the host of Codex, of OpenCode `openai/...` models and of an `openai` judge without `baseURL`, and for the reporting site's render-time redaction); a ClusterRole `skill-ci-controller-crds-<namespace>` reads CustomResourceDefinitions to detect Cilium and the Agent Sandbox extensions. The runner Role covers `agents.x-k8s.io` Sandboxes, `extensions.agents.x-k8s.io` SandboxClaims, pods and `pods/exec`.
- Controller Service `skill-ci-controller`: port `8080` named `webhook`, port `8081` named `metrics`, port `8082` named `reports` (the reporting site, flag `--reports-addr`, empty disables it; Helm `reports.enabled`); pod label `app.kubernetes.io/name: skill-ci-controller`.

## Cost guard

Controller flags `--max-retries` (default 1, see "Retry"; every retry Job spends its trials again), `--max-concurrent-jobs` (default 4, deployed with 3 so 3 Jobs x 2 trials fit a small cluster; extra SkillEvals stay Pending), `--trial-budget` (max trials per controller lifetime, default 200; trials = attempts x 2 arms; Harbor's trial retries on rate limits are not counted, see "Retry"), `--dry-run` (create SkillEvals in phase Planned, log matrix size, create no Jobs).
