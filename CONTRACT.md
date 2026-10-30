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
  verdict: Helps                    # Helps | Neutral | Hurts | Obsolete | Regressed
  reportDigest: sha256:...          # optional: the eval report attached to the skill digest in Harbor
  message: ...
  completedAt: ...
```

Printer columns: Skill, Version, Model, Phase, With, Without, Lift, Verdict, Age; Report (`status.reportDigest`) at priority 1 (`-o wide`).

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

The runner image provides the rest as defaults (overridable env): `REGISTRY_CONFIG=/auth/config.json` (mount secret `harbor-pull` there), `REGISTRY_PLAIN_HTTP=true`, `SANDBOX_NAMESPACE=skill-ci`, `TASK_IMAGE_REGISTRY=registry.digitalocean.com/skill-ci-dirien`, `TASK_IMAGE_PULL_SECRET=docr`, `SANDBOX_CPUS=1`, `SANDBOX_MEMORY_MB=2048` (per-trial sandbox size), `TRIAL_CONCURRENCY=2` (trials at once per Job). Sandboxes created by a runner pod are owned by that pod, so cancelling a run deletes them.

The container sets `terminationMessagePolicy: FallbackToLogsOnError`. On completion it writes one JSON object to `/dev/termination-log` and exits 0:

```json
{"withSkill": 0.95, "withoutSkill": 0.36, "lift": 0.59, "agent": "claude-code", "model": "claude-haiku-5-5", "report": "sha256:..."}
```

`report` is optional: the digest of the report the runner attached to the evaluated skill digest (see "Eval reports"). The controller stores it in `status.reportDigest`. A runner that cannot attach leaves the field out; the eval still succeeds.

If no score was produced it writes `{"error": "<reason>"}` and exits 1.

The `skill-eval-runner` ServiceAccount and Role are in `runner/job.yaml` today; the controller manifests own them from now on.

## Eval reports

After scoring, the runner attaches the SkillEvaluator output to the evaluated skill digest as an OCI referrer, so it outlives the pod:

```text
oras attach --artifact-type application/vnd.skillci.report.v1+json \
  --annotation skillci.dev/model=<model> --annotation skillci.dev/agent=<agent> --annotation skillci.dev/lift=<+0.59> \
  <SKILL_REF> report.html:text/html result.json:application/json [BENCHMARK.md:text/markdown]
```

`SKILL_REF` is pinned to the digest, so the subject is exactly the version that was scored. Re-running an eval adds another referrer. `make report SKILL=<skill> VERSION=<vX.Y.Z> [MODEL=<model>] OUT=reports/` lists the referrers (`oras discover`) and downloads the newest report per model.

## Triggers

1. **Skill push.** Harbor webhook policy on project `skills`, events `PUSH_ARTIFACT`, target `http://skill-ci-controller.skill-ci.svc.cluster.local:8080/webhook/harbor`, payload format `Default`. The controller acts only when the pushed tag matches `^v\d+\.\d+\.\d+$`. Other tags (`beta`, `stable`) are ignored. For each model in the list it creates a SkillEval unless one already exists for the same digest and model (dedupe by labels).
2. **Model list change.** The controller watches ConfigMap `skill-ci-models`. For each added model entry it creates SkillEvals for every skill digest currently tagged `beta` or `stable` in Harbor (`trigger: model-change`).

## Policy

Per SkillEval verdict (SkillEvaluator bands): `lift >= +0.05` Helps, `lift <= -0.10` Hurts, otherwise Neutral; Neutral with `withoutSkill >= 0.80` is Obsolete. The version-over-version gate below overrides the verdict with `Regressed` on the models that scored worse than the stable version; the policy itself always reads the no-skill bands, never the stored verdict.

Per digest, once every required model has a Succeeded SkillEval:

- Tag the digest `beta` when it is the newest evaluated version (controller manages `beta`).
- **Promote**: lift `>= +0.10` on all required models, and no regression against the current stable version (below): add tag `stable` (move it from any other digest of that skill).
- **Regression gate**: if another digest of the skill is tagged `stable`, compare the candidate's with-skill score with that digest's, per model. A model counts when it is required and both digests have a Succeeded SkillEval for it. If the candidate scores lower by more than `--regression-tolerance` (default `0.05`, scores compared at two decimals, a drop of exactly the tolerance passes) on any such model, the digest is not promoted and the current stable stays. The candidate still gets `beta`. Its SkillEvals get condition `Regressed` (reason `WorseThanStable`, message names the model, both versions and both scores), a Warning event, and verdict `Regressed` on the models that dropped. Optional models never gate. With no current stable version, or when the candidate is the stable digest, nothing is compared.
- **Newest wins ties**: an older version never takes `stable` from a newer stable version unless that newer version scores worse than it (the regression gate, applied the other way round). Without this rule two equally good versions would swap the tag on every reconcile.
- **Demote**: lift `<= -0.10` on any required model: remove tag `stable` from that digest.
- **Obsolete**: verdict Obsolete on all required models: flag only (metric + condition), never delete.

Verdicts depend on other digests (the stable one), so when a SkillEval reaches `Succeeded`, or the policy changes a tag, the controller re-enqueues the other `Succeeded` SkillEvals of the same skill (all digests). They re-run the policy and converge; a re-run that changes nothing writes nothing and enqueues nothing.

Tag changes go through the Harbor REST API (`POST /api/v2.0/projects/skills/repositories/<skill>/artifacts/<digest>/tags`, `DELETE .../artifacts/<digest>/tags/<tag>`). Unlike `oras tag`, these do not fire `PUSH_ARTIFACT`. Thresholds are controller flags (`--helps-threshold`, `--hurts-threshold`, `--promote-threshold`, `--demote-threshold`, `--obsolete-baseline`, `--regression-tolerance`).

## Metrics (controller `:8081/metrics`)

Gauges labeled `skill`, `version`, `agent`, `model`: `skillci_lift`, `skillci_with_score`, `skillci_without_score`. Gauge `skillci_stable{skill,version}` (1 when tagged stable). Gauge `skillci_obsolete{skill,version}`. Gauge `skillci_regressed{skill,version}` (1 when the version scores worse than the stable version beyond the tolerance). Counter `skillci_skillevals_total{phase}`.

## Webhook auth

Secret `harbor-webhook` (key `token`). The Harbor webhook policy sets `auth_header` to `Bearer <token>`; the controller rejects requests whose `Authorization` header doesn't match (env `WEBHOOK_TOKEN` from that secret). If `WEBHOOK_TOKEN` is empty, the controller logs a warning and accepts all requests.

## Build and deploy

- `controller/Dockerfile`, build context `controller/`.
- `runner/Dockerfile`, build context repo root.
- Images are built by the in-cluster BuildKit (`buildctl --addr tcp://127.0.0.1:1234` through `kubectl -n skill-ci port-forward svc/buildkitd 1234:1234`).
- All controller-side manifests (CRD, controller Deployment and Service, RBAC, runner ServiceAccount and Role) live under `config/` with a `config/kustomization.yaml`; deploy is `kubectl apply -k config/`.
- Controller Service `skill-ci-controller`: port `8080` named `webhook`, port `8081` named `metrics`; pod label `app.kubernetes.io/name: skill-ci-controller`.

## Cost guard

Controller flags `--max-concurrent-jobs` (default 4, deployed with 3 so 3 Jobs x 2 trials fit a small cluster; extra SkillEvals stay Pending), `--trial-budget` (max trials per controller lifetime, default 200; trials = attempts x 2 arms), `--dry-run` (create SkillEvals in phase Planned, log matrix size, create no Jobs).
