# Stage runbook: Skills Are Packages Now

Talk: "Skills Are Packages Now. Your Platform Should Treat Them That Way." KubeAuto, 25 minutes.

The point of the demo: a skill is an OCI artifact in a registry, and a Kubernetes controller evaluates every new version the way a CI system tests a package, then moves the `beta` and `stable` tags on its own.

## Say this out loud

- The three skills are demo material written for this talk. `acme-release-notes` teaches a made-up format (`ACME-RN/2`), which is why no model knows it without the skill and the lift is large.
- `acme-release-notes` v1.2.0 is a deliberately worse rewrite whose rules contradict its own evals. It exists to show the gate firing.
- Each score is one SkillEval: 3 eval cases, 3 attempts per arm, so 9 trials per arm. It is a small sample, not a benchmark. The pre-baked results below were recorded before the statistical gate, with one attempt per arm.
- Every SkillEval takes 11 to 13 minutes. Nothing below waits for a run except the one optional live trigger, and its result arrives after the talk.

## The model list: agents, versions and what gates

The model list (`demo/models.yaml`, ConfigMap `skill-ci-models`) names the harness as well as the model. An entry is **agent + agent version + model**:

| Agent | Release | Model | Gates promotion? |
| --- | --- | --- | --- |
| `claude-code` | 2.1.292 | `claude-opus-5-5` | yes (`required: true`) |
| `codex` | 0.161.0 | `openai/gpt-6.1-sol` (placeholder, see the file) | no (`required: false`) |
| `opencode` | 1.19.1 | `ollama-cloud/gpt-oss:120b` | no (`required: false`) |

Gating is the `required` flag of each entry. Optional entries run, show up in `kubectl get skillevals`, on the reporting site and in Grafana, and never hold a version back. Flip one live: `make demo-model-swap MODE=require AGENT=codex` (and `MODE=optional AGENT=codex` back). The runs that exist are re-judged at once, so a skill that Codex does not lift stops being promoted, with no new run.

Needs the keys in `llm-keys`: `anthropic` always (it is the judge, whatever agent runs), `openai` for Codex, `ollama` for the OpenCode entry. Without a key the run fails in a few seconds with the name of the missing key, before any sandbox starts.

## The state the beats rely on

Results read from the cluster on 2026-10-31 (`kubectl -n skill-ci get skillevals -o wide`), release 0.7.x, the model list above: Claude Code 2.1.292 with `claude-opus-5-5` (required), Codex 0.161.0 with `openai/gpt-6.1-sol` and OpenCode 1.19.1 with `openrouter/qwen/qwen3.8-flash` (both optional). Every run has 3 eval cases × 3 attempts per arm (9 trials each); the lift column carries its 95% confidence interval.

| Skill and version | Claude Code + Opus 5.5 (required) with / without / lift | Codex + gpt-6.1-sol with / without / lift | OpenCode + Qwen 3.8 Flash with / without / lift | Outcome |
| --- | --- | --- | --- | --- |
| acme-release-notes v1.0.0 | 0.95 / 0.36 / +0.59 [+0.58, +0.60] | 0.72 / 0.47 / +0.25 [+0.24, +0.27] | 0.96 / 0.39 / +0.57 [+0.55, +0.58] | Helps on all three |
| acme-release-notes v1.1.0 | 0.98 / 0.37 / +0.61 [+0.59, +0.63] | 0.72 / 0.45 / +0.27 [+0.24, +0.31] | 0.95 / 0.38 / +0.57 [+0.56, +0.58] | Helps, takes over `stable` |
| acme-release-notes v1.2.0 | 0.69 / 0.36 / +0.33 [+0.31, +0.34] | 0.47 / 0.46 / +0.01 [-0.01, +0.04] | 0.72 / 0.37 / +0.35 [+0.32, +0.37] | Regressed on the required entry, held at `beta` |
| k8s-resource-naming v1.0.0 | 0.94 / 0.37 / +0.57 [+0.54, +0.59] | 0.65 / 0.45 / +0.20 [+0.18, +0.22] | 0.93 / 0.39 / +0.54 [+0.50, +0.58] | Helps, `stable` |
| k8s-resource-naming v1.1.0 | Rejected (Tier 1) | Rejected (Tier 1) | Rejected (Tier 1) | No trials, no tokens; `stable` stays on v1.0.0 |
| python-docstrings v1.0.0 | 0.96 / 0.58 / +0.38 [+0.33, +0.43] | 0.59 / 0.46 / +0.13 [+0.09, +0.16] | 0.96 / 0.57 / +0.39 [+0.32, +0.47] | Helps, `stable` |

Expected tags in Harbor (`make status`, bottom block; digests differ per push):

```text
python-docstrings        sha256:b1e4c917ad0d    beta, stable, v1.0.0
k8s-resource-naming      sha256:d9dbbda999ce    beta, stable, v1.0.0
k8s-resource-naming      sha256:7af9cf706b31    v1.1.0
acme-release-notes       sha256:dbbcbc8d27a8    v1.0.0
acme-release-notes       sha256:7d8ab5a5f659    stable, v1.1.0
acme-release-notes       sha256:dc63d710bde5    beta, v1.2.0
```

Pre-baked for beat 7: four more Claude Code runs on release 2.1.300 (v1.1.0 and v1.2.0 of acme-release-notes, v1.0.0 of the other two), from the live rehearsal of `make demo-model-swap MODE=bump`; the model list itself is back on 2.1.292. They are required runs, so they count: v1.2.0 is Regressed on both releases, everything else `Helps`. `kubectl -n skill-ci get skillevals` shows 22 SkillEvals.

To rebuild this state from scratch: `make reset`, `make seed`, then `make demo-push VERSION=v1.1.0`, `make demo-push SKILL=k8s-resource-naming VERSION=v1.1.0`, and once v1.1.0 holds `stable`, `make demo-push VERSION=v1.2.0`; for the 2.1.300 runs, `make demo-model-swap MODE=bump TO=2.1.300`, wait, then `MODE=restore`. About an hour and a half of cluster time.

Four things to know before you talk about these numbers:

1. **The lift of v1.2.0 is still positive** on Claude Code (+0.33 over no skill). A check against the no-skill baseline alone would have promoted it. The regression gate compares its with-skill score with the stable version: 0.69 against 0.98, a difference of -0.29 with a 95% interval of [-0.31, -0.27], entirely below the -0.05 tolerance. That contrast is the best moment in the talk.
2. **Codex and OpenCode are optional, so they never gate.** On v1.2.0 Codex falls to `Neutral` (+0.01, interval across zero) and OpenCode stays `Helps` at 0.72 although stable scored 0.95. That is the policy working as written (CONTRACT.md, "Regression gate"), not a missed regression. Say it before someone asks.
3. **The same skill lifts the three agents by very different amounts.** On acme-release-notes v1.0.0, Claude Code and OpenCode gain about +0.58, Codex +0.25; on python-docstrings, +0.38 and +0.39 against +0.13. With the skill, the small Qwen model scores as high as Opus. The lift belongs to a skill, an agent release and a model together, never to the skill alone.
4. **No run in this state is Neutral on a required entry.** On the earlier list (Claude Code with Haiku as a required model) python-docstrings gained only +0.03 on Haiku and stayed on `beta`. On the current list every required lift clears +0.10, so python-docstrings holds `stable`. Beat 6 shows the closest current example.

## Controller version check (do this first)

For the statistical gate (Inconclusive, confidence intervals) and the agent versions run controller 0.10.0 with the runner and sandbox images of the same release (`make deploy` installs all three) and apply the CRD first (`kubectl apply --server-side -f charts/skill-ci-controller/crds/`, which `make deploy` does). Older runs have no trial scores and are judged by the fixed thresholds. SkillEvals from before 0.6.0 have no `spec.agentVersion`: the controller treats them as entries without a version, and `kubectl get skillevals` shows an empty `AGENTVER` for them. Applying the pinned list of `demo/models.yaml` on top of them adds the pinned entries, so every `beta` and `stable` skill runs again; for a clean demo state run `make reset` first.

Controller images before 0.2.1 have two defects that show up exactly in this state. Both are fixed in 0.2.1 (`controller/internal/controller/siblings.go` and `policy.go`, see CONTRACT.md):

- Verdicts were only re-evaluated when one of the candidate's own SkillEvals reconciled, so a candidate could keep a stale verdict after the stable version's scores changed.
- An older version that qualified for promotion took `stable` away from a newer one, and the newer one took it back. On the live cluster with 0.2.0, `kubectl -n skill-ci logs deploy/skill-ci-controller | grep "changed tag"` showed the `stable` tag moving back and forth between the v1.0.0 and v1.1.0 digests (ten tag operations in 18 seconds after a controller restart).

Make sure 0.2.1 or later runs (`helm -n skill-ci list` shows the app version). On the cluster, 0.2.1 has been verified: no tag changes after the rollout. The check that you have it: after the controller has been up for a minute, the `grep "changed tag"` command above prints nothing new, and `make status` shows `stable` on v1.1.0 and nowhere else for acme-release-notes.

## Pre-show checklist

Plan for a clean run on a cluster that has been up for at least 90 minutes. Rebuilding the pre-run state from scratch takes that long (see "Rebuild the state").

One hour before:

1. Cluster up and reachable.

   ```bash
   export KUBECONFIG=~/.kube/skill-ci.yaml   # cd infra && pulumi stack output kubeconfig --show-secrets > ~/.kube/skill-ci.yaml
   kubectl get nodes
   kubectl -n skill-ci get pods
   kubectl -n monitoring get pods
   ```

   Expect every pod `Running` (finished runner pods show `Completed`) and every node `Ready`. A node showing `SchedulingDisabled` was seen on 2026-10-30; two ready nodes are enough.

2. Controller image and flapping check (see above).

   ```bash
   kubectl -n skill-ci get deploy skill-ci-controller -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}{.spec.template.spec.containers[0].args}{"\n"}'
   kubectl -n skill-ci logs deploy/skill-ci-controller --since=2m | grep "changed tag"
   ```

3. State matches the table above.

   ```bash
   make status
   ```

   Expect 15 SkillEvals, all `Succeeded`, and the tag block shown above. If it does not, go to "Recovery".

4. The LLM key exists (only matters for the live trigger).

   ```bash
   kubectl -n skill-ci get secret llm-keys harbor-credentials harbor-pull harbor-webhook
   ```

   The sandbox image (about 1 GB compressed) is pulled once per node. A node the autoscaler added after the last run has not got it yet, so the first sandbox on it waits for the pull. Before a live trigger, run one eval so the nodes you rely on have the image.

5. Reports on disk (they are needed offline, in case the network fails).

   ```bash
   make report SKILL=acme-release-notes VERSION=v1.1.0 OUT=reports/
   ls reports/*/report.html
   ```

   Expect three reports, one per entry: `acme-release-notes-v1.1.0-claude-code@2.1.292_claude-opus-5-5`, `...-codex@0.161.0_openai_gpt-6.1-sol` and `...-opencode@1.19.1_openrouter_qwen_qwen3.8-flash`, each with `report.html`, `tier3.html`, `result.json`, `validate.json` and `BENCHMARK.md`. Every run in the current state has a stored report, the Rejected ones included.

6. Prepare the live trigger (optional, see beat 2). Make a copy of a skill with a real edit, so Harbor sees a new digest:

   ```bash
   rm -rf /tmp/live && mkdir -p /tmp/live && cp -r skills/python-docstrings /tmp/live/
   printf '\nPrefer one-line summaries under 80 characters.\n' >> /tmp/live/python-docstrings/SKILL.md
   ```

Ten minutes before, open these. Large font, dark theme, one task per terminal:

| Where | What | Command |
| --- | --- | --- |
| Terminal 1 | Main stage terminal, repo root, `KUBECONFIG` exported | `make status`, `kubectl ...` |
| Terminal 2 | Watch | `kubectl -n skill-ci get skillevals -w` |
| Terminal 3 | Controller log | `kubectl -n skill-ci logs deploy/skill-ci-controller -f` |
| Terminal 4 | Grafana port-forward, leave it running | `make dashboard` |
| Terminal 5 | Reporting site port-forward, leave it running | `make reports-ui` |
| Browser tab 1 | Grafana, dashboard `skill-ci` | `http://localhost:3000`, user `admin` |
| Browser tab 4 | Reporting site, the closing screen | `http://localhost:8082` |
| Browser tab 2 | Fallback: the published run report | <https://claude.ai/artifact/YNMduYoqFyxCMrsjvmywZp> |
| Browser tab 3 | One local report | `reports/acme-release-notes-v1.1.0-claude-opus-5-5/report.html` |

Grafana password:

```bash
kubectl -n monitoring get secret grafana-admin -o jsonpath='{.data.admin-password}' | base64 -d
```

Check that the `skill-ci` dashboard shows the lift table filled in and refresh it once. If the table is empty, Prometheus lost the scrape (24 hour retention, and the controller exposes gauges only for SkillEvals it reconciled since its last start). Restarting the controller repopulates them within a minute: `kubectl -n skill-ci rollout restart deploy/skill-ci-controller`.

## The beats

Times are a guide for a 25 minute slot: 5 minutes of slides before the first beat, about 15 minutes of demo, the rest for questions.

### Beat 1: skills live in a registry (about 2 minutes)

```bash
make status
```

The audience sees the top block (SkillEvals) and the bottom block, "Skills in Harbor": one row per skill digest with its tags. Scroll to the bottom block.

Say: these are OCI artifacts in Harbor, pushed with `oras push --artifact-type application/vnd.agentskill.v1`. The tags `beta` and `stable` are the release channels. Nobody types them. The controller moves them.

Expected: the six rows shown above. `acme-release-notes` has three digests, only v1.1.0 carries `stable`, v1.2.0 carries `beta`. `k8s-resource-naming` v1.1.0 has its version tag and nothing else.

### Beat 2: a push triggers an evaluation (about 3 minutes)

Option A, the live trigger (spends LLM tokens and three Job slots, finishes in 11 to 13 minutes, so the result arrives after the talk):

```bash
demo/push-skill.sh python-docstrings v1.0.1 /tmp/live/python-docstrings
```

The audience sees, in terminals 2 and 3: the Harbor `PUSH_ARTIFACT` webhook handled with `created":3` in the controller log, then three new SkillEvals appear `Pending`, then `Running` as Jobs start (`kubectl -n skill-ci get jobs,pods` shows the runner pods). Say: the controller made one SkillEval per entry on the list (Claude Code, Codex, OpenCode), for this digest. Each one is a Job that pulls the skill by digest and runs the with and without arms in sandboxes.

Do not wait for it. Say: "that takes 12 minutes, so here is one I prepared earlier" and go to beat 3.

Option B, no live trigger: show the finished object instead.

```bash
kubectl -n skill-ci get skilleval acme-release-notes-v1-0-0-claude-code-2-1-292-claude-opus-5-5 -o yaml
```

Point at `spec.digest`, `spec.trigger: push`, `spec.agentVersion: 2.1.292`, `status.withSkill: "0.95"`, `status.withoutSkill: "0.36"`, `status.lift: "+0.59"`, `status.liftCI: "[+0.58, +0.60]"`, `status.n: 9/9`, `status.verdict: Helps`.

The push path itself was exercised on 2026-10-31 with `make demo-push` (v1.1.0 and v1.2.0, about 12 minutes per round with three entries); the live trigger from a copied directory (`demo/push-skill.sh` with a directory argument) has not been rehearsed on stage yet.

### Beat 3: the lift of a good skill (about 2 minutes)

```bash
kubectl -n skill-ci get skillevals -l skillci.dev/skill=acme-release-notes -o wide
```

The audience sees nine rows: v1.0.0, v1.1.0 and v1.2.0, one per agent. For v1.0.0: Claude Code with Opus 5.5 0.95 with, 0.36 without, +0.59; Codex with gpt-6.1-sol 0.72 / 0.47 / +0.25; OpenCode with Qwen 3.8 Flash 0.96 / 0.39 / +0.57. All `Helps`, every interval far above zero.

Say: the same skill, three agents, one number each. Without the skill no agent gets the house format right (0.36 to 0.47). With it, Claude Code and OpenCode reach 0.95, Codex 0.72. The small Qwen model with the skill scores like Opus. The format is arbitrary, so a stronger model does not need the skill less.

### Beat 4: a better version takes over `stable` (about 2 minutes)

```bash
kubectl -n skill-ci get skillevals -l skillci.dev/skill=acme-release-notes,skillci.dev/agent=claude-code -o wide
make status
```

The audience sees v1.1.0 at 0.98 with the skill on Claude Code (lift +0.61, interval [+0.59, +0.63]), and +0.27 on Codex, +0.57 on OpenCode. In the Harbor block, `stable` sits on v1.1.0 and v1.0.0 only has its version tag.

Say: v1.1.0 adds two rules (ticket-ID order, an explicit empty section). Its lift on the required entry is above +0.10 and the whole interval is above zero, so the controller moved `stable` to it through the Harbor API. The condition on the object says so:

```bash
kubectl -n skill-ci get skilleval acme-release-notes-v1-1-0-claude-code-2-1-292-claude-opus-5-5 -o jsonpath='{.status.conditions[?(@.type=="PolicyApplied")].reason}{"\n"}'
```

Expected output: `Promoted`. (The message next to it says `no tag changes` once the tags are in place; the move itself shows in the controller log as `changed tag` and in `make status`.)

### Beat 5: the regression gate (about 4 minutes, the centrepiece)

```bash
kubectl -n skill-ci get skillevals -l skillci.dev/skill=acme-release-notes -o wide | grep v1-2-0
```

The audience sees, for v1.2.0 (columns trimmed):

```text
v1.2.0  claude-code  2.1.292  claude-opus-5-5                Succeeded  0.69  0.36  +0.33  Regressed
v1.2.0  codex        0.161.0  openai/gpt-6.1-sol             Succeeded  0.47  0.46  +0.01  Neutral
v1.2.0  opencode     1.19.1   openrouter/qwen/qwen3.8-flash  Succeeded  0.72  0.37  +0.35  Helps
```

Say: lift is still positive on Claude Code, so a "does it beat no skill" test passes. But stable scored 0.98 with the skill, and this version scores 0.69. The gate compares with the version you run today, so it blocks the promotion and marks the verdict `Regressed`. Then the reason, in the object:

```bash
kubectl -n skill-ci get skilleval acme-release-notes-v1-2-0-claude-code-2-1-292-claude-opus-5-5 -o jsonpath='{.status.conditions[?(@.type=="Regressed")].message}{"\n"}'
```

Expected output (recorded 2026-10-31):

```text
claude-code@2.1.292/claude-opus-5-5 scores 0.69 with v1.2.0, stable v1.1.0 scored 0.98: difference -0.29, 95% CI [-0.31, -0.27] (tolerance 0.05)
```

The whole interval is below the tolerance: nine trials per arm, and the drop is the same in every one of them.

Then `make status`: v1.2.0 carries `beta` (it is the newest) but not `stable`. Point out the two optional rows: Codex drops to `Neutral` (+0.01, interval across zero) and OpenCode stays `Helps` at 0.72 against a stable 0.95. Neither gates, so neither appears in the message. Kubernetes Events with reason `WorseThanStable` exist too, but events expire after an hour, so do not rely on `kubectl get events` for a pre-run state.

Be honest: v1.2.0 was written to fail. The real-world version of this is a well-meant edit that fixes one case and breaks two.

#### Regressed versus Inconclusive (the talking point: the gate needs evidence, not one number)

Say: the same skill version scored 0.95 on one run and 0.87 on the next. A gate that fires at "more than 0.05 below stable" would have held a good version back on that noise. So the gate does not compare two numbers. Each run keeps its 9 trial scores per arm and the controller puts a 95% confidence interval around the difference.

The measured v1.2.0 run above is the clear case: `Regressed`. A noisier drop is judged differently. The two rows below show both outcomes side by side, with the stable version at 0.95:

```bash
kubectl -n skill-ci get skillevals -l skillci.dev/skill=acme-release-notes -o wide | grep v1-2-0
```

```text
v1.2.0  claude-haiku-5-5    Succeeded  0.70  0.36  +0.34  Regressed     ...  [+0.30, +0.38]  9/9
v1.2.0  claude-sonnet-5-5   Succeeded  0.87  0.31  +0.56  Inconclusive  ...  [+0.38, +0.74]  9/9
```

These two rows are illustrative (the numbers come from the unit-test fixtures in `controller/internal/policy` and the worked example in CONTRACT.md), not a measured state; the measured Regressed run is in beat 5. What the two conditions say:

- **Regressed**: `claude-code/claude-haiku-5-5 scores 0.70 with v1.2.0, stable v1.1.0 scored 0.95: difference -0.25, 95% CI [-0.26, -0.24] (tolerance 0.05)`. The whole interval is below zero: the drop is real. Warning event `WorseThanStable`.
- **Inconclusive**: `claude-code/claude-sonnet-5-5: 0.87 with v1.2.0 vs 0.95 with stable v1.1.0, difference -0.08, 95% CI [-0.18, +0.03]; not enough evidence to call it a regression`. The drop is bigger than the 0.05 tolerance, but the interval reaches above zero, so noise could explain it. Normal event `NotEnoughEvidence`.

```bash
kubectl -n skill-ci get skillevals -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Inconclusive")].message}{"\n"}{end}' | grep .
```

Both keep `stable` where it is and give the version `beta`. The difference is what you tell the author: Regressed means fix it, Inconclusive means run more trials before deciding either way. On the reporting site the Inconclusive verdict is neutral (hollow marker, the word "Inconclusive"), and the skill page says "Not proven" with the numbers and "The stable version stays; more trials would show whether the drop is real."

The same rule works for promotion: a lift of +0.12 whose interval runs from -0.05 to +0.29 does not earn `stable`, because the skill may not help at all. The PolicyApplied message and the skill page say "not beyond noise". Set `controller.thresholds.confidence` to `"0.99"` to ask for more evidence, `"0.90"` for less.

### Beat 6: the same skill, a much smaller lift (about 2 minutes)

```bash
kubectl -n skill-ci get skillevals -l skillci.dev/skill=python-docstrings -o wide
```

The audience sees: Claude Code 0.96 with, 0.58 without, +0.38; Codex 0.59 / 0.46 / +0.13, interval [+0.09, +0.16]; OpenCode 0.96 / 0.57 / +0.39. All `Helps`; in the Harbor block `python-docstrings` holds `beta` and `stable`.

Say: Google-style docstrings are something models already half know, so the baseline is high (0.46 to 0.58) and the lift smaller than for the made-up release-note format. On Codex the lift is +0.13 and the lower end of its interval, +0.09, sits below the +0.10 promotion bar. Codex is optional here, so it does not matter; make it required (beat 8) and this is the kind of result that decides whether a skill earns `stable`. Compare k8s-resource-naming (arbitrary team conventions): +0.57, +0.20, +0.54, `stable`.

Not in this state: a version that stays on `beta` because a required lift is below +0.10. On the earlier model list (Claude Code with Haiku as a required model) python-docstrings gained only +0.03 on Haiku and was held there. To show that live, add a weaker required entry before the talk and let it run.

### Beat 7: the harness changed (about 3 minutes)

The model did not change, the tool around it did. Claude Code ships often, and a new release can change how a skill is found and followed. The list pins the release, so a new one is a new entry.

```bash
make demo-model-swap MODE=bump TO=2.1.308
kubectl -n skill-ci get skillevals -w
```

The audience sees in terminals 2 and 3: the model list change in the controller log (`model list changed`), then one new SkillEval per `beta` or `stable` digest, `Pending` and then `Running`. The `AGENTVER` column shows `2.1.308` on them and `2.1.292` on the earlier runs:

```text
NAME                                                          SKILL                VERSION  AGENT        AGENTVER  MODEL            PHASE
acme-release-notes-v1-1-0-claude-code-2-1-292-claude-opus-5-5 acme-release-notes   v1.1.0   claude-code  2.1.292   claude-opus-5-5  Succeeded
acme-release-notes-v1-1-0-claude-code-2-1-308-claude-opus-5-5 acme-release-notes   v1.1.0   claude-code  2.1.308   claude-opus-5-5  Running
```

Say: nobody pushed a skill. The tool changed, and every skill version in a release channel is tested against it, the way a package is rebuilt against a new compiler. The old results stay: same skill, same model, two harness releases, two entries. If the new release makes a skill worse, the regression gate compares it with the stable version **on the same harness release**, so the answer is about the skill, not about the harness.

This spends tokens and takes 11 to 13 minutes per skill version, so it is a second live trigger: start it and say "here is one I prepared earlier", then show the finished entries on the reporting site (`/skills/acme-release-notes`: one column per `agent@version` and model) or in Grafana (the `agent_version` label). Rehearsed live on 2026-10-31 with `TO=2.1.300` (2.1.308 was younger than the two-week rule then; it is 18 days old on talk day): four new runs, one per `beta` and `stable` digest, 12 to 15 minutes each. Results on 2.1.300: acme-release-notes v1.1.0 +0.60, k8s-resource-naming v1.0.0 +0.61, python-docstrings v1.0.0 +0.38, all `Helps`; acme-release-notes v1.2.0 `Regressed` again, and its message now names both harness releases, each against stable on the same release:

```text
claude-code@2.1.292/claude-opus-5-5 scores 0.69 with v1.2.0, stable v1.1.0 scored 0.98: difference -0.29, 95% CI [-0.31, -0.27]; claude-code@2.1.300/claude-opus-5-5 scores 0.69 with v1.2.0, stable v1.1.0 scored 0.97: difference -0.28, 95% CI [-0.31, -0.26] (tolerance 0.05)
```

Undo with `make demo-model-swap MODE=restore`. The runs on the bumped release stay and keep counting until they are deleted: `kubectl -n skill-ci delete skillevals -l skillci.dev/agent-version=2.1.308` (or `make reset` for the whole state).

### Beat 8: other harnesses, and who gates (about 2 minutes)

```bash
kubectl -n skill-ci get skillevals -l skillci.dev/skill=acme-release-notes -o custom-columns=RUN:.metadata.name,AGENT:.spec.agent,VER:.status.agentVersion,MODEL:.spec.model,LIFT:.status.lift,REQUIRED:.spec.required
```

Say: Codex and OpenCode run the same skill with their own models: Codex 0.161.0 with gpt-6.1-sol, OpenCode 1.19.1 with Qwen 3.8 Flash through OpenRouter. They are optional here, so a bad score does not block anything. Then flip the switch:

```bash
make demo-model-swap MODE=require AGENT=codex
```

The controller updates `spec.required` of the Codex runs and re-judges every digest: if Codex's lift is below +0.10, or its with-skill score fell against stable, the digest stops being promoted, with no new run. `MODE=optional AGENT=codex` puts it back. With Codex required, acme-release-notes v1.2.0 gains a second reason to stay on `beta` (0.47 against 0.72 on stable). Both optional routes ran end to end on 2026-10-31: Codex against the OpenAI API, OpenCode against OpenRouter (it needs an OpenRouter API key, not a provisioning key; the runner rejects those before any work).

### Beat 9: metrics and reports (about 3 minutes)

Grafana (browser tab 1, dashboard `skill-ci`): the lift matrix per skill version, `agent@version` and model, the with-skill and no-skill scores, the stable table. Say: the controller exposes `skillci_lift`, `skillci_stable`, `skillci_regressed` and friends, scraped by Prometheus. The dashboard JSON is in the repo.

Then the evidence behind one number:

```bash
make report SKILL=acme-release-notes VERSION=v1.1.0 OUT=reports/
```

The audience sees a table of three report referrers (sha256 digest, creation time, agent, agent version, model, lift +0.61, +0.27, +0.57), then three downloads, each with `report.html`, `tier3.html`, `result.json`, `validate.json` and `BENCHMARK.md`. Open `reports/acme-release-notes-v1.1.0-claude-code@2.1.292_claude-opus-5-5/report.html` in the browser.

Say: the full SkillEvaluator report is attached to the skill digest as an OCI referrer, so it survives the pod and travels with the artifact.

Then the closing screen, browser tab 4, the reporting site served by the controller (`make reports-ui`): the runs list with every SkillEval, the release channels, and a skill page (`/skills/acme-release-notes`) that says in words why v1.2.0 is held back and v1.1.0 is on stable. Open the run `acme-release-notes-v1-1-0-claude-code-2-1-292-claude-opus-5-5` for the six dimensions and the full SkillEvaluator report. Every run in the current state has one.

### Close

Return to the first slide of the stack: Harbor, a controller, Jobs, sandboxes. The summary line: the same pipeline you already run for packages, applied to skills.

## Fallbacks

If the cluster or the network fails, do not debug on stage.

0. If only the reporting site is down, run `make report-page` and open `reports/index.html`, the offline version of the same content.
1. Switch to browser tab 2, the published run report: <https://claude.ai/artifact/YNMduYoqFyxCMrsjvmywZp>. It has the lift matrix, the release channels and what the policy decided. Careful: it was published while the controller was flapping, so its "Release channels" table can disagree with its own "v1.1.0 promoted to stable" text. Talk to the decisions and the matrix, not the channels table.
2. Open the local reports from `reports/`, which need no network.
3. Show the table in "The state the beats rely on" from this file.

## Recovery

| Symptom | Fix |
| --- | --- |
| `make status` hangs or errors on the Harbor block | The toolbox pod is not ready: `kubectl -n skill-ci rollout status deploy/toolbox`. The SkillEval block works without it: `kubectl -n skill-ci get skillevals -o wide` |
| Reporting site unreachable | The port-forward died, or the controller image is older than 0.3.0 (no `--reports-addr`). Run `make reports-ui` again, or use `make report-page` |
| Grafana unreachable | The port-forward died. Stop terminal 4 and run `make dashboard` again |
| Dashboard empty | `kubectl -n skill-ci rollout restart deploy/skill-ci-controller`, wait a minute, refresh |
| `stable` moving back and forth in the controller log | The controller is older than 0.2.1. Run `make deploy` with a newer release (see "Controller version check") |
| A webhook did not fire, so a pushed version has no SkillEvals | `make seed RETRIGGER=1` re-fires the push webhook for the v1.0.0 tags. For another version, `demo/retrigger.sh <skill> <version>`. The controller dedupes by digest and model, so nothing is evaluated twice |
| State is wrong beyond repair | `make reset`, then rebuild (below). This deletes every result, and rebuilding takes about 90 minutes. Never do it within an hour of the show |

`make reset` deletes all SkillEvals and Jobs, removes the `beta` and `stable` tags and every version except v1.0.0 from Harbor, and restores the model list from `demo/models.yaml` (Claude Code 2.1.292 with opus as the only required entry, plus optional Codex and OpenCode entries). That is a different list from the one the pre-baked table was made with, so rebuild the state afterwards.

### Rebuild the state

Do this the day before, never on the day. With the old list, fifteen SkillEvals run three at a time (the controller allows three Jobs, `maxConcurrentJobs: 3`), each taking 11 to 13 minutes, so expect about 70 minutes of pure runtime plus queueing.

```bash
make reset
make seed RETRIGGER=1                 # v1.0.0 of all three skills: one SkillEval per entry of the model list
# wait until every v1.0.0 is tagged beta or stable: make status
make demo-model-swap MODE=add         # an optional model (claude-sonnet-5-5): one SkillEval per tagged v1.0.0 digest
make demo-push VERSION=v1.2.0         # the regression: 3 SkillEvals
make demo-push VERSION=v1.1.0         # the improvement: 3 SkillEvals
make status
```

Watch with `kubectl -n skill-ci get skillevals -w`. Check the end state against the table above. Not verified end to end: the sequence is assembled from the scripts and from how the current state came about (opus was added to the list after the v1.0.0 runs, which is why those opus rows have `trigger: model-change`). Compare the final `make status` with the table and retrigger what is missing.

## Teardown

This deletes the cluster and Harbor. Run it by hand, after the talk, when you are sure.

```bash
. ~/.pulumi-local/skill-ci-env.sh && cd infra && pulumi destroy
```

`make down` refuses on purpose.
