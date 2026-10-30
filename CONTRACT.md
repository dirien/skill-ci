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
- **Rejected**: a run whose Tier 1 or Tier 2 failed ends `Succeeded` with verdict `Rejected` and usually no scores (Tier 3 was skipped; a Succeeded run without scores and without a failed tier is marked `Failed` instead). It counts as finished for "every required model has a result". If any required model is Rejected, the digest gets no `beta` and no `stable`, loses `stable` if it holds it, and is never the "newest evaluated version" that takes `beta` (the older version keeps it). The Rejected run gets condition `Rejected` (reason `Tier1Failed` or `Tier2Failed`, message the findings) and a Warning event with the same reason. Rejected runs are left out of the regression comparison. Rejected optional models never gate. A termination message from a runner without `tiers` still works: the tiers are unknown, nothing is Rejected.

Verdicts depend on other digests (the stable one), so when a SkillEval reaches `Succeeded`, or the policy changes a tag, the controller re-enqueues the other `Succeeded` SkillEvals of the same skill (all digests). They re-run the policy and converge; a re-run that changes nothing writes nothing and enqueues nothing.

Tag changes go through the Harbor REST API (`POST /api/v2.0/projects/skills/repositories/<skill>/artifacts/<digest>/tags`, `DELETE .../artifacts/<digest>/tags/<tag>`). Unlike `oras tag`, these do not fire `PUSH_ARTIFACT`. Thresholds are controller flags (`--helps-threshold`, `--hurts-threshold`, `--promote-threshold`, `--demote-threshold`, `--obsolete-baseline`, `--regression-tolerance`).

## Metrics (controller `:8081/metrics`)

Gauges labeled `skill`, `version`, `agent`, `model`: `skillci_lift`, `skillci_with_score`, `skillci_without_score`. Gauge `skillci_stable{skill,version}` (1 when tagged stable). Gauge `skillci_obsolete{skill,version}`. Gauge `skillci_regressed{skill,version}` (1 when the version scores worse than the stable version beyond the tolerance). Gauge `skillci_publication{skill,version,model,status}` is 1 for the publication status of the run (`passed`, `failed` or `incomplete`; one series per skill, version and model, replaced when a rerun changes the status, absent for runs without a publication status). Gauge `skillci_rejected{skill,version}` is 1 when a required model's run failed Tier 1 or Tier 2. Counter `skillci_skillevals_total{phase}` counts phase transitions. Gauge `skillci_skillevals{phase}` is the current number of SkillEvals per phase (Pending, Running, Succeeded, Failed, Planned; recounted from the cache every 5 s, zero when none). When a SkillEval is deleted the controller removes its `skillci_lift`, `skillci_with_score` and `skillci_without_score` series, and the `skillci_publication` series of its skill, version and model, and the `skillci_stable`, `skillci_obsolete`, `skillci_regressed` and `skillci_rejected` series of its version once no SkillEval of that skill and version remains, so dashboards never show the numbers of a deleted run.

## Reporting site (controller `:8082`)

Read-only, server-rendered, no JavaScript, no external resources. Data: SkillEvals from the manager's cache, channel tags from the Harbor REST API (cached 5 s), reports from the OCI referrers named by `status.reportDigest` (fetched with the registry API using `HARBOR_USERNAME`/`HARBOR_PASSWORD`, cached in memory by digest, 32 MiB). A run without `reportDigest` shows "not stored".

| Route | Content |
|---|---|
| `/` | Counts (including Rejected versions), release channels per skill (Stable, Beta, Held back, Rejected), all runs newest first with Tiers (T1 T2 T3 markers) and Publication columns. Filters: `skill`, `model`, `verdict`, `phase`, `publication` |
| `/skills/{skill}` | Versions x models (with, without, lift, verdict, tier markers), channel tags, policy decision per version ("Rejected by Tier 1: <first finding>") |
| `/runs/{name}` | One SkillEval: scores, the SkillEvaluator tiers with findings, conditions, the six SkillEvaluator dimensions when a report is stored. A run without scores (Tier 3 skipped) shows "Tier 3 not run: Tier 1 rejected this version, so no live trials were spent." and no score or dimension blocks |
| `/runs/{name}/report` | The stored `report.html`, as is (`text/html`) |
| `/runs/{name}/result.json` | The stored `result.json` (`application/json`, attachment) |
| `/healthz` | `ok` |

Unknown runs and skills give a 404 page. Policy decisions use the controller thresholds.

## Webhook auth

Secret `harbor-webhook` (key `token`). The Harbor webhook policy sets `auth_header` to `Bearer <token>`; the controller rejects requests whose `Authorization` header doesn't match (env `WEBHOOK_TOKEN` from that secret). If `WEBHOOK_TOKEN` is empty, the controller logs a warning and accepts all requests.

## Build and deploy

- `controller/Dockerfile`, build context `controller/`.
- `runner/Dockerfile`, build context repo root.
- Images are built by the in-cluster BuildKit (`buildctl --addr tcp://127.0.0.1:1234` through `kubectl -n skill-ci port-forward svc/buildkitd 1234:1234`).
- All controller-side manifests (CRD, controller Deployment and Service, RBAC, runner ServiceAccount and Role) live under `config/` with a `config/kustomization.yaml`; deploy is `kubectl apply -k config/`.
- Controller Service `skill-ci-controller`: port `8080` named `webhook`, port `8081` named `metrics`, port `8082` named `reports` (the reporting site, flag `--reports-addr`, empty disables it; Helm `reports.enabled`); pod label `app.kubernetes.io/name: skill-ci-controller`.

## Cost guard

Controller flags `--max-concurrent-jobs` (default 4, deployed with 3 so 3 Jobs x 2 trials fit a small cluster; extra SkillEvals stay Pending), `--trial-budget` (max trials per controller lifetime, default 200; trials = attempts x 2 arms), `--dry-run` (create SkillEvals in phase Planned, log matrix size, create no Jobs).
