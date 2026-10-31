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
| `llm-keys` | `anthropic` (required); optional: `openai`, `openai-base-url`, `nvidia`, `ollama`, `openrouter` (see "LLM keys") | runner Job |
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

`required` is the gating knob, per entry. Changing it on an existing entry (not a new one) updates `spec.required` of that entry's existing SkillEvals, which re-runs the tag policy for them: make an entry required and its results gate promotion of the versions already evaluated; make it optional and they stop gating. The runs of removed entries stay and keep gating while they are `required`, so delete them (or `make reset`) when you retire a required entry.

### Models per agent

| Agent | `model` | Provider route |
| --- | --- | --- |
| `claude-code` | a Claude model, `claude-opus-5-5` | `ANTHROPIC_API_KEY` |
| `codex` | an OpenAI model, `openai/<id>` (the prefix is removed for the CLI) | `OPENAI_API_KEY` and `OPENAI_BASE_URL` (default `https://api.openai.com/v1`) |
| `opencode` | `<provider>/<model id>`: `anthropic/...` runs natively with the Anthropic key; `ollama-cloud/...`, `openrouter/...`, `nvidia/...`, `openai/...` run through that provider's OpenAI-compatible endpoint with its key | see "LLM keys" |

The judge that scores every run is Anthropic whatever the agent under test (`SKILL_EVAL_LLM_PROVIDER=anthropic`).

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
  message: ...
  completedAt: ...
```

Printer columns: Skill, Version, Agent (`spec.agent`), AgentVer (`status.agentVersion`), Model, Phase, With, Without, Lift, Verdict, Tier1, Age; Lift CI (`status.liftCI`), N (`status.n`), Publication and Report (`status.reportDigest`) at priority 1 (`-o wide`).

The statistics fields are present only when the runner sent trial scores (see "Statistics"). The controller recomputes them from `status.samples` whenever it reconciles a finished run, so a changed `--confidence` shows in `liftCI` without a new run. `samples` holds two decimals as strings (CRDs have no floats), up to 50 per arm.

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
| `ANTHROPIC_API_KEY` | from secret `llm-keys` key `anthropic` |
| `OPENAI_API_KEY` | from secret `llm-keys` key `openai` (optional) |
| `OPENAI_BASE_URL` | from secret `llm-keys` key `openai-base-url` (optional) |
| `NVIDIA_API_KEY` | from secret `llm-keys` key `nvidia` (optional) |
| `OLLAMA_API_KEY` | from secret `llm-keys` key `ollama` (optional) |
| `OPENROUTER_API_KEY` | from secret `llm-keys` key `openrouter` (optional). Must be an API key: before any work the runner asks OpenRouter (`GET /api/v1/key`) and fails the run when it is a provisioning key, which answers every chat call with 401 "User not found." |

Pods and Jobs carry the labels `skillci.dev/skill`, `/digest`, `/agent`, `/model` and, for a pinned entry, `/agent-version`.

The runner image provides the rest as defaults (overridable env): `REGISTRY_CONFIG=/auth/config.json` (mount secret `harbor-pull` there), `REGISTRY_PLAIN_HTTP=true`, `SANDBOX_NAMESPACE=skill-ci`, `SANDBOX_IMAGE=ghcr.io/dirien/skill-ci-sandbox:<the runner's release>` (set when the runner image is built, from the release tag), `SANDBOX_CPUS=1`, `SANDBOX_MEMORY_MB=2048` (per-trial sandbox size), `TRIAL_CONCURRENCY=2` (trials at once per Job), `TIMEOUT_MULTIPLIER=2` (scales Harbor's step timeouts; matters for a non-default `AGENT_VERSION`, which is installed in every sandbox). Sandboxes created by a runner pod are owned by that pod, so cancelling a run deletes them. No registry, no pull secret and no builder are part of the defaults: see "Sandbox image" for the one optional path that uses them (`TASK_IMAGE_REGISTRY`).

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

`report` is optional: the digest of the report the runner attached to the evaluated skill digest (see "Eval reports"). The controller stores it in `status.reportDigest`. A runner that cannot attach leaves the field out; the eval still succeeds.

If no score was produced it writes `{"error": "<reason>"}` and exits 1.

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

Only `anthropic` is required: it is the judge (`SKILL_EVAL_LLM_PROVIDER=anthropic` is set whenever the key exists), whatever agent runs. SkillEvaluator hands the agent only the credentials of its route, never the judge key.

- **Codex** needs `OPENAI_API_KEY` and `OPENAI_BASE_URL` together (SkillEvaluator refuses a partial pair under an Anthropic judge). The runner sets `OPENAI_BASE_URL=https://api.openai.com/v1` when only the key is present. The model is passed as given (`--agent-model codex=openai/<id>`) and must be one the key can use; SkillEvaluator checks it against `<base url>/models`.
- **OpenCode** with an Anthropic judge accepts only `anthropic/<model>` in stock SkillEvaluator 0.6.0. For `ollama-cloud/...`, `openrouter/...`, `nvidia/...` and `openai/...` models the runner uses the OpenAI-compatible gateway route that `skillevaluator-patch/` hunks 6 and 8 to 12 open: it presents the provider's key as `OPENAI_API_KEY` and its endpoint (`https://ollama.com/v1`, `https://openrouter.ai/api/v1`, `https://integrate.api.nvidia.com/v1`, `OPENAI_BASE_URL` or `https://api.openai.com/v1`) as `OPENAI_BASE_URL` to the agent only, runs the model as `openai/<id>` with SkillEvaluator's `SkillEvaluatorGatewayOpenCode` (Chat Completions through `@ai-sdk/openai-compatible`; hunk 6 allows that wrapper on the `ack` backend and hunk 12 forwards the key into the sandbox, which Harbor does not do for the wrapper's `skillevaluator-gateway/` model namespace), and keeps the real OpenAI or NVIDIA key for Tier 2 embeddings through `SKILL_EVAL_EMBEDDING_PROVIDER=openai-compatible` (or `nv_build`). A missing key for the provider fails the run before any work with `opencode model ... needs <KEY>`.
- A termination message and a log never contain a key: the runner redacts the values of all of the keys above.

## Eval reports

After scoring, the runner attaches the SkillEvaluator output to the evaluated skill digest as an OCI referrer, so it outlives the pod:

```text
oras attach --artifact-type application/vnd.skillci.report.v1+json \
  --annotation skillci.dev/model=<model> --annotation skillci.dev/agent=<agent> --annotation skillci.dev/agent-version=<version> \
  --annotation skillci.dev/lift=<+0.59> \
  <SKILL_REF> report.html:text/html result.json:application/json [BENCHMARK.md:text/markdown]
```

`SKILL_REF` is pinned to the digest, so the subject is exactly the version that was scored. Re-running an eval adds another referrer. `make report SKILL=<skill> VERSION=<vX.Y.Z> [MODEL=<model>] OUT=reports/` lists the referrers (`oras discover`) and downloads the newest report per agent@version/model entry.

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

1. **Skill push.** Harbor webhook policy on project `skills`, events `PUSH_ARTIFACT`, target `http://skill-ci-controller.skill-ci.svc.cluster.local:8080/webhook/harbor`, payload format `Default`. The controller acts only when the pushed tag matches `^v\d+\.\d+\.\d+$`. Other tags (`beta`, `stable`) are ignored. For each entry in the list it creates a SkillEval unless one already exists for the same digest, agent, agent version and model (dedupe by labels).
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

Gauges labeled `skill`, `version`, `agent`, `agent_version`, `model` (`agent_version` is the pinned release, else the one the runner reported, else `unknown`; two entries differing in agent, agent version or model are two series): `skillci_lift`, `skillci_with_score`, `skillci_without_score`, and, for runs with trial scores, `skillci_lift_ci_lower` and `skillci_lift_ci_upper` (the lift interval at `--confidence`; absent for runs without trial scores). Gauge `skillci_stable{skill,version}` (1 when tagged stable). Gauge `skillci_obsolete{skill,version}`. Gauge `skillci_regressed{skill,version}` (1 when the version scores worse than the stable version beyond the tolerance and beyond noise). Gauge `skillci_inconclusive{skill,version}` (1 when it scores worse beyond the tolerance but the interval includes 0, and no model regressed beyond noise). Gauge `skillci_publication{skill,version,agent,agent_version,model,status}` is 1 for the publication status of the run (`passed`, `failed` or `incomplete`; one series per skill, version and entry, replaced when a rerun changes the status, absent for runs without a publication status). Gauge `skillci_rejected{skill,version}` is 1 when a required model's run failed Tier 1 or Tier 2. Counter `skillci_skillevals_total{phase}` counts phase transitions. Counter `skillci_retries_total{reason}` counts runs started again after an infrastructure failure (`reason` is `env_start_timeout`, `connection_lost`, `agent_install`, `agent_setup` or `job_lost`; all five series exist at 0 from startup). Gauge `skillci_skillevals{phase}` is the current number of SkillEvals per phase (Pending, Running, Succeeded, Failed, Planned; recounted from the cache every 5 s, zero when none). When a SkillEval is deleted the controller removes its `skillci_lift`, `skillci_lift_ci_lower`, `skillci_lift_ci_upper`, `skillci_with_score` and `skillci_without_score` series, and the `skillci_publication` series of its entry, and the `skillci_stable`, `skillci_obsolete`, `skillci_regressed`, `skillci_inconclusive` and `skillci_rejected` series of its version once no SkillEval of that skill and version remains, so dashboards never show the numbers of a deleted run.

## Reporting site (controller `:8082`)

Read-only, server-rendered, no JavaScript, no external resources. Data: SkillEvals from the manager's cache, channel tags from the Harbor REST API (cached 5 s), reports from the OCI referrers named by `status.reportDigest` (fetched with the registry API using `HARBOR_USERNAME`/`HARBOR_PASSWORD`, cached in memory by digest, 32 MiB). A run without `reportDigest` shows "not stored".

| Route | Content |
| --- | --- |
| `/` | Counts (including Rejected versions), release channels per skill (Stable, Beta, Held back, Rejected), all runs newest first with Agent (`agent@version`), Model, Tiers (T1 T2 T3 markers) and Publication columns. Filters: `skill`, `agent` (`agent@version`), `model`, `verdict`, `phase`, `publication` |
| `/skills/{skill}` | Versions x entries (one column per `agent@version` and model: with, without, lift, verdict, tier markers), channel tags, policy decision per version ("Rejected by Tier 1: <first finding>") |
| `/runs/{name}` | One SkillEval (title with model and `agent@version`, an Agent version row that says when the trials ran another release than the spec pins): scores, the SkillEvaluator tiers with findings, conditions, the six SkillEvaluator dimensions when a report is stored. A run without scores (Tier 3 skipped) shows "Tier 3 not run: Tier 1 rejected this version, so no live trials were spent." and no score or dimension blocks. A run with `status.retries > 0` shows "Retried once after an infrastructure failure: <message of the Retried condition>" ("Retried N times ..." for more) next to the status message |
| `/runs/{name}/report` | The stored `report.html`, as is (`text/html`) |
| `/runs/{name}/result.json` | The stored `result.json` (`application/json`, attachment) |
| `/healthz` | `ok` |

Lifts show their evidence wherever they appear (runs table, skill matrix cells, run page): `+0.59` with `95% CI +0.41 to +0.73, n=9/9` (the level is `--confidence`, `n` the trials with/without), left out for runs without trial scores. Verdict `Inconclusive` is neutral (hollow marker, text "Inconclusive"). The policy decision says in plain words when a version is "Not proven" (a drop that may be noise: the numbers, "The stable version stays; more trials would show whether the drop is real") or "Not promoted" because a lift is not beyond noise. The "How to read this" legend explains the interval in two sentences.

Unknown runs and skills give a 404 page. Policy decisions use the controller thresholds.

## Retry

A run that fails for an infrastructure reason is started again, because that failure says nothing about the skill. A run with a real result is never retried: retrying bad answers until they pass would bias the score. This mirrors Harbor 0.25's error classification (infrastructure errors retried, agent errors not).

- **Never retried**: a run with scores, a Tier 1 or Tier 2 failure (`Rejected`), a failed Job that also wrote a result, a runner `error` that comes with scores, and everything the classifier does not recognise. Unknown means not transient.
- **Transient** (package `internal/retry`, `Transient(message, jobFailedWithoutMessage)`), for a run that ends `Failed` without a result:
  - the Job failed and the runner left no termination message, or a pod of the Job was evicted/disrupted and left no readable result (`job_lost`);
  - the failure message names a Kubernetes-level loss: `DeadlineExceeded`, `Evicted`, `Preempt*`, `FailedScheduling`, `Unschedulable`, `ImagePullBackOff`, `ErrImagePull` (`job_lost`);
  - the runner error (the SkillEvaluator `execution_errors` of all trials, joined with a semicolon and a space after `no scores in result.json:`) consists only of transient parts. It is split at every `<Name>Error:` token and every part must qualify, so one agent error in the mix makes the run a real result:
    - `EnvironmentStartTimeoutError` (`env_start_timeout`);
    - any part containing `Connection to remote host was lost` (`connection_lost`);
    - `NonZeroAgentExitCodeError` whose failed command starts like the agent install (`apt-get`, `apk`, `yum`, `dnf`, `set -euo pipefail; if command -v apk`, `npm install`, `pip install`, `curl ... | sh`) (`agent_install`) or the agent setup (`mkdir -p $CLAUDE_CONFIG_DIR`) (`agent_setup`). Only the start of the command counts, because the agent's own task command carries the task text, which may mention anything.
  - `AgentTimeoutError` is the agent's own failure on its task and is never transient, nor is a `NonZeroAgentExitCodeError` of any other command.
- **Mechanics**: the budget is `--max-retries` (default 1, Helm `controller.maxRetries`; 0 disables). When a run would become `Failed`, the failure is transient and `status.retries < --max-retries`, the controller sets `status.retries` +1 and the condition `Retried` (`True`, reason `InfrastructureFailure`, message the failure of the previous Job), writes phase `Pending`, emits a Normal event `InfrastructureFailure`, and only then deletes the old Job (propagation Background; the status write comes first so a lost race never reads a missing Job as a failure). The run then goes through the normal Pending path with a new Job named `<skilleval>-r<n>`: it waits for a slot (`--max-concurrent-jobs`) and spends its trials from `--trial-budget` again. The failed attempt keeps what it spent: the budget counts every Job started. When the budget is exhausted the retry stays `Pending` ("trial budget exhausted"). `completedAt` is set only when the run ends. The `Retried` condition stays after the run finishes; a second transient failure ends `Failed` with that failure as `status.message`.

## Webhook auth

Secret `harbor-webhook` (key `token`). The Harbor webhook policy sets `auth_header` to `Bearer <token>`; the controller rejects requests whose `Authorization` header doesn't match (env `WEBHOOK_TOKEN` from that secret). If `WEBHOOK_TOKEN` is empty, the controller logs a warning and accepts all requests.

## Build and deploy

- `controller/Dockerfile`, build context `controller/`.
- `runner/Dockerfile`, build context repo root. Build arg `SKILL_CI_VERSION` sets the runner's default `SANDBOX_IMAGE` tag.
- `sandbox/Dockerfile`, build context `sandbox/`.
- Nothing is built in the cluster. A `vX.Y.Z` tag runs `.github/workflows/release.yaml`: GoReleaser publishes the controller image and binaries, the workflow builds and Cosign-signs the runner and sandbox images (with SBOM and provenance) and the Helm chart, all as `ghcr.io/dirien/...:X.Y.Z`. The chart's default image tags are its appVersion. `make deploy` installs the working-tree chart with the tags of the newest release (`TAG=...` overrides).
- All controller-side manifests (CRD, controller Deployment and Service, RBAC, runner ServiceAccount and Role) live under `config/` with a `config/kustomization.yaml`; deploy is `kubectl apply -k config/`.
- Controller Service `skill-ci-controller`: port `8080` named `webhook`, port `8081` named `metrics`, port `8082` named `reports` (the reporting site, flag `--reports-addr`, empty disables it; Helm `reports.enabled`); pod label `app.kubernetes.io/name: skill-ci-controller`.

## Cost guard

Controller flags `--max-retries` (default 1, see "Retry"; every retry Job spends its trials again), `--max-concurrent-jobs` (default 4, deployed with 3 so 3 Jobs x 2 trials fit a small cluster; extra SkillEvals stay Pending), `--trial-budget` (max trials per controller lifetime, default 200; trials = attempts x 2 arms), `--dry-run` (create SkillEvals in phase Planned, log matrix size, create no Jobs).
