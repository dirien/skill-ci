# Architecture

skill-ci is built from open source parts plus three components of its own: the controller, the runner and the sandbox image. This page is the overview; the interfaces between the parts are specified in [CONTRACT.md](../CONTRACT.md).

Two projects share the name Harbor. In these docs "Harbor" is the goharbor registry that stores the skills. The trial runner that SkillEvaluator uses is called the Harbor framework.

## Parts

| Part | Where it runs | What it does |
| --- | --- | --- |
| Harbor (goharbor) | namespace `harbor` | Stores every skill version as an OCI artifact in the project `skills` (repository = skill name), sends `PUSH_ARTIFACT` webhooks, holds the eval reports as OCI referrers of the skill digest |
| skill-ci controller | namespace `skill-ci` | Receives the webhook, watches the model list, creates `SkillEval` objects and one runner Job per run, applies the tag policy through the Harbor REST API, serves metrics and the reporting site |
| skill-ci runner | one Job per SkillEval | Pulls the skill with ORAS, runs SkillEvaluator Tier 1, 2 and 3, attaches the report to the skill digest, reports scores in its termination message |
| SkillEvaluator 0.6.0 + [patch](../skillevaluator-patch/) | inside the runner | Static and security checks, deduplication, live with/without-skill trials, lift and publication assessment |
| Harbor framework | inside the runner | Runs the agent trials for SkillEvaluator |
| [harbor-agent-sandbox](../harbor-agent-sandbox/) | inside the runner | Harbor framework environment that runs each trial as an Agent Sandbox `Sandbox` from the sandbox image |
| Agent Sandbox v1.0.9 | namespace `agent-sandbox-system` | Turns a `Sandbox` or a `SandboxClaim` on a warm pool into a trial pod |
| skill-ci sandbox image | every trial pod | Python, the verifier packages and the Claude Code, Codex and OpenCode CLIs at their pins |
| Cilium (optional) | the CNI | Enforces the network mode of a run through a `CiliumNetworkPolicy` |
| Prometheus, Grafana, an OTLP backend (optional) | anywhere | Collect and show the `skillci_*` metrics |

skill-ci adds three CRDs in the group `skillci.dev/v1alpha1`:

- `SkillEval` (short name `se`): one evaluation of one skill digest on one model list entry, with its scores, statistics, verdict and conditions.
- `SandboxProfile` (short name `sbp`): the image, size and warm pool of the trial sandboxes.
- `SkillCIConfig` (short name `scc`): the platform settings of one install, read from the object named `default`.

## From push to tag

1. A skill author pushes a version: `oras push <host>/skills/<name>:vX.Y.Z --artifact-type application/vnd.agentskill.v1 <name>/`. `demo/push-skill.sh` does the same and also copies the network mode the skill declares in its `SKILL.md` into OCI annotations; with plain `oras push`, add them with `--annotation`.
2. Harbor sends `PUSH_ARTIFACT` to `http://skill-ci-controller.skill-ci.svc.cluster.local:8080/webhook/harbor` with a bearer token from the Secret `harbor-webhook`. The controller acts only on tags that match `^v\d+\.\d+\.\d+$`, so `beta` and `stable` are ignored.
3. For each entry of the model list (agent + agent version + model) the controller creates a `SkillEval`, unless one exists for the same digest, agent, agent version and model. It deduplicates by digest, never by tag.
4. Before the first Job of a run the controller reads the skill's network annotations from Harbor, applies the `SkillCIConfig`, creates the `CiliumNetworkPolicy` when Cilium enforces the mode, and picks the warm pool of the run's `SandboxProfile`. At most `--max-concurrent-jobs` Jobs run at once (Helm `controller.maxConcurrentJobs`, deployed with 3), and `--trial-budget` caps the trials per controller lifetime.
5. The runner Job pulls the skill by digest, runs the Tier 1 and 2 gate, and, if the gate passes, the live Tier 3 trials: 3 attempts per eval case and arm by default, each in a sandbox claimed from the warm pool.
6. The runner attaches `report.html`, `result.json` and, when present, `BENCHMARK.md` to the skill digest as an OCI referrer, then writes the scores, the per-trial samples, the tier results and the report digest to its termination message.
7. The controller stores the result in the `SkillEval` status, computes the lift interval, sets the verdict, and runs the tag policy for the skill. Tags move through the Harbor REST API, which fires no new webhook. Because a verdict depends on the other versions of the skill, the controller re-runs the policy for the other finished SkillEvals of the same skill when one finishes or a tag moves.

A run that fails for an infrastructure reason (an evicted pod, a sandbox that did not start in time, a lost connection) is started again once by default (`--max-retries`). A run with a real result is never retried. The rules are in [CONTRACT.md, "Retry"](../CONTRACT.md).

Before that, Harbor retries single trials inside the run. A trial that failed because the provider rate limited the agent (`ApiRateLimitError`) runs again up to `TRIAL_RETRIES` times (runner env, default 2, `0` turns it off), after 20 s, then 40 s, and never more than 300 s. Only the last try counts, and `--trial-budget` does not count the retries. Harbor spots a rate limit by the words in the agent's output, which include the task text, so a task about rate limits can also be retried this way, at most `TRIAL_RETRIES` times.

## Triggers

- A skill push with a `vX.Y.Z` tag evaluates that digest on every model list entry.
- A change to the ConfigMap `skill-ci-models` evaluates every digest currently tagged `beta` or `stable` on each added entry (`trigger: model-change`). A new agent release is a new entry, so it counts as added. A changed `required` flag updates the existing runs of that entry and re-runs the policy.
- `trigger: manual` marks a SkillEval created by hand.

## Eval reports

The SkillEvaluator output outlives the runner pod because it is stored next to the skill in Harbor:

```text
oras attach --artifact-type application/vnd.skillci.report.v1+json \
  --annotation skillci.dev/model=<model> --annotation skillci.dev/agent=<agent> --annotation skillci.dev/agent-version=<version> \
  --annotation skillci.dev/lift=<+0.59> \
  <SKILL_REF> report.html:text/html result.json:application/json [BENCHMARK.md:text/markdown]
```

`SKILL_REF` is pinned to the digest, so the report belongs to exactly the version that was scored. `make report` downloads them and the reporting site shows them, see [observability.md](observability.md).

## The sandbox image

No image is built while an evaluation runs. For a plain skill (no `requirements.txt` or `apt-packages.txt`, no custom `evals/environment/Dockerfile`), the Dockerfiles that SkillEvaluator generates for every eval case and arm contain the same steps, so `sandbox/Dockerfile` builds one generic image with those steps and the three agent CLIs. `harbor-agent-sandbox` starts each trial from it and replays the task-specific parts at start: it uploads the `COPY` sources (the skill under test, or an empty directory for the baseline) and applies `WORKDIR`, `ENV` and `USER`.

A task that needs a step the image does not have fails before any pod is created, with a message that names the line, and the run is not retried. The fix is to add the step to `sandbox/Dockerfile` and its digest to `COVERED_RUNS` in `harbor_agent_sandbox/dockerfile.py`, or to build task images in a registry of your own (`TASK_IMAGE_REGISTRY`). [CONTRACT.md, "Sandbox image"](../CONTRACT.md) has the full rules, including the caveats of the build path.

The image is built once per release by the release workflow and published to GHCR. It is about 3 GB on disk (1 GB compressed), so the first trial on a new node waits for the pull (a sandbox waits up to 10 minutes to become ready) and later trials start from the node's cache.

## Deployment options

- `deploy/install.sh` or the steps in [deploy/README.md](../deploy/README.md): Helm and kubectl on any cluster.
- The Helm chart alone, `oci://ghcr.io/dirien/charts/skill-ci-controller`, when Harbor and Agent Sandbox exist: [charts/skill-ci-controller/README.md](../charts/skill-ci-controller/README.md).
- `kubectl apply -k config/` as a plain-manifest alternative to the chart. On a new cluster apply it twice, because the `SandboxProfile` and `SkillCIConfig` objects need their CRDs established first.
- The Pulumi stack in `infra/` for a reference DOKS cluster: [reference-stack.md](reference-stack.md).
