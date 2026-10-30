# skill-ci contract

Shared interfaces for every part of skill-ci. Change this file first, then the code.

## Names

| Thing | Value |
|---|---|
| Namespace | `skill-ci` |
| Harbor (goharbor) in-cluster URL | `http://harbor.harbor.svc.cluster.local` (plain HTTP, also its `externalURL`) |
| Harbor project for skills | `skills` (repository = skill name) |
| Skill artifact | `oras push <host>/skills/<name>:vX.Y.Z --artifact-type application/vnd.agentskill.v1 <name>/` (directory named exactly like the skill) |
| Container images (runner, controller, task images) | `registry.digitalocean.com/skill-ci-dirien/<image>:<tag>`, pull secret `docr` |
| Controller image | `registry.digitalocean.com/skill-ci-dirien/skill-ci-controller:<tag>` |
| Runner image | `registry.digitalocean.com/skill-ci-dirien/skill-ci-runner:<tag>` |
| BuildKit | `tcp://buildkitd.skill-ci:1234` |

## Secrets (namespace `skill-ci`)

| Secret | Keys | Used by |
|---|---|---|
| `llm-keys` | `anthropic` (required), `openai` (optional) | runner Job |
| `docr` | `.dockerconfigjson` | image pulls, task image pushes |
| `harbor-credentials` | `username`, `password` (Harbor robot account or admin) | controller (Harbor REST API) |
| `harbor-pull` | `.dockerconfigjson` for `harbor.harbor.svc.cluster.local` | runner Job (oras pull) |

## Model list

ConfigMap `skill-ci-models`, key `models.yaml`:

```yaml
models:
  - agent: claude-code
    model: claude-haiku-5-5
    required: true
  - agent: claude-code
    model: claude-sonnet-5-5
    required: true
```

`agent` is a SkillEvaluator/Harbor agent (`claude-code`, `codex`, `opencode`). A pair `agent/model` is the identity of a model entry.

## CRD: `SkillEval` (`skillci.dev/v1alpha1`, plural `skillevals`, short name `se`)

One object per skill digest x model. Name: `<skill>-<version-with-dots-as-dashes>-<model>` truncated to 63 chars plus a 6-char hash suffix if truncated.

```yaml
spec:
  skill: acme-release-notes         # repository name in project "skills"
  version: v1.0.0                   # the semver tag that was pushed
  digest: sha256:...                # manifest digest; identity of the version
  agent: claude-code
  model: claude-haiku-5-5
  required: true
  attempts: 1                       # trials per arm
  trigger: push                     # push | model-change | manual
status:
  phase: Pending                    # Pending | Running | Succeeded | Failed | Planned (dry run)
  jobName: ...
  withSkill: "0.95"                 # strings, two decimals
  withoutSkill: "0.36"
  lift: "+0.59"
  verdict: Helps                    # Helps | Neutral | Hurts | Obsolete
  message: ...
  completedAt: ...
```

Printer columns: Skill, Version, Model, Phase, With, Without, Lift, Verdict, Age.

Labels on every SkillEval: `skillci.dev/skill`, `skillci.dev/digest` (hex without `sha256:`, first 32 chars), `skillci.dev/model`.

## Runner Job contract

The controller creates one `batch/v1` Job per SkillEval (owner reference to the SkillEval, `backoffLimit: 0`, `ttlSecondsAfterFinished: 3600`, ServiceAccount `skill-eval-runner`, imagePullSecrets `docr`). Container `skill-eval` runs the runner image with no args and these env vars:

| Env | Example |
|---|---|
| `SKILL_REF` | `harbor.harbor.svc.cluster.local/skills/acme-release-notes@sha256:...` |
| `SKILL_NAME` | `acme-release-notes` |
| `AGENT` | `claude-code` |
| `MODEL` | `claude-haiku-5-5` |
| `ATTEMPTS` | `1` |
| `ANTHROPIC_API_KEY` | from secret `llm-keys` key `anthropic` |
| `OPENAI_API_KEY` | from secret `llm-keys` key `openai` (optional) |

The runner image provides the rest as defaults (overridable env): `REGISTRY_CONFIG=/auth/config.json` (mount secret `harbor-pull` there), `REGISTRY_PLAIN_HTTP=true`, `SANDBOX_NAMESPACE=skill-ci`, `TASK_IMAGE_REGISTRY=registry.digitalocean.com/skill-ci-dirien`, `TASK_IMAGE_PULL_SECRET=docr`.

The container sets `terminationMessagePolicy: FallbackToLogsOnError`. On completion it writes one JSON object to `/dev/termination-log` and exits 0:

```json
{"withSkill": 0.95, "withoutSkill": 0.36, "lift": 0.59, "agent": "claude-code", "model": "claude-haiku-5-5"}
```

If no score was produced it writes `{"error": "<reason>"}` and exits 1.

The `skill-eval-runner` ServiceAccount and Role are in `runner/job.yaml` today; the controller manifests own them from now on.

## Triggers

1. **Skill push.** Harbor webhook policy on project `skills`, events `PUSH_ARTIFACT`, target `http://skill-ci-controller.skill-ci.svc.cluster.local:8080/webhook/harbor`, payload format `Default`. The controller acts only when the pushed tag matches `^v\d+\.\d+\.\d+$`. Other tags (`beta`, `stable`) are ignored. For each model in the list it creates a SkillEval unless one already exists for the same digest and model (dedupe by labels).
2. **Model list change.** The controller watches ConfigMap `skill-ci-models`. For each added model entry it creates SkillEvals for every skill digest currently tagged `beta` or `stable` in Harbor (`trigger: model-change`).

## Policy

Per SkillEval verdict (SkillEvaluator bands): `lift >= +0.05` Helps, `lift <= -0.10` Hurts, otherwise Neutral; Neutral with `withoutSkill >= 0.80` is Obsolete.

Per digest, once every required model has a Succeeded SkillEval:

- Tag the digest `beta` when it is the newest evaluated version (controller manages `beta`).
- **Promote**: lift `>= +0.10` on all required models: add tag `stable` (move it from any other digest of that skill).
- **Demote**: lift `<= -0.10` on any required model: remove tag `stable` from that digest.
- **Obsolete**: verdict Obsolete on all required models: flag only (metric + condition), never delete.

Tag changes go through the Harbor REST API (`POST /api/v2.0/projects/skills/repositories/<skill>/artifacts/<digest>/tags`, `DELETE .../artifacts/<digest>/tags/<tag>`). Unlike `oras tag`, these do not fire `PUSH_ARTIFACT`. Thresholds are controller flags.

## Metrics (controller `:8081/metrics`)

Gauges labeled `skill`, `version`, `agent`, `model`: `skillci_lift`, `skillci_with_score`, `skillci_without_score`. Gauge `skillci_stable{skill,version}` (1 when tagged stable). Gauge `skillci_obsolete{skill,version}`. Counter `skillci_skillevals_total{phase}`.

## Cost guard

Controller flags `--max-concurrent-jobs` (default 4; extra SkillEvals stay Pending), `--trial-budget` (max trials per controller lifetime, default 200; trials = attempts x 2 arms), `--dry-run` (create SkillEvals in phase Planned, log matrix size, create no Jobs).
