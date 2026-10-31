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

The numbers below were recorded before agent versions existed, on the old list: claude-code with `claude-haiku-5-5` and `claude-sonnet-5-5` (both required) and `claude-opus-5-5` (optional). Beats 1 to 6 describe that state. After a `make reset` the list is the one above, so rows, columns and the "required" models differ until you rebuild the state.

Pre-run results, read from the cluster on 2026-10-30 (`kubectl -n skill-ci get skillevals -o wide`). Models are `claude-haiku-5-5`, `claude-sonnet-5-5` (both required) and `claude-opus-5-5` (optional), all under `claude-code`.

| Skill and version | Haiku with / without / lift | Sonnet with / without / lift | Opus with / without / lift | Outcome |
| --- | --- | --- | --- | --- |
| acme-release-notes v1.0.0 | 0.95 / 0.35 / +0.60 | 0.95 / 0.32 / +0.63 | 0.95 / 0.36 / +0.59 | Helps on all three |
| acme-release-notes v1.1.0 | 0.95 / 0.37 / +0.58 | 0.95 / 0.31 / +0.64 | 0.95 / 0.37 / +0.58 | Helps, takes over `stable` |
| acme-release-notes v1.2.0 | 0.69 / 0.36 / +0.33 | 0.70 / 0.31 / +0.39 | 0.71 / 0.37 / +0.34 | Regressed on haiku and sonnet, held at `beta` |
| k8s-resource-naming v1.0.0 | 0.93 / 0.36 / +0.57 | 0.95 / 0.33 / +0.62 | 0.95 / 0.38 / +0.57 | Helps, `stable` |
| python-docstrings v1.0.0 | 0.62 / 0.59 / +0.03 | 0.95 / 0.52 / +0.43 | 0.96 / 0.55 / +0.41 | Neutral on haiku, stays `beta` |

Expected tags in Harbor (`make status`, bottom block):

```text
python-docstrings        sha256:1237a487439e    beta, v1.0.0
k8s-resource-naming      sha256:4a22596e7004    beta, stable, v1.0.0
acme-release-notes       sha256:f5e465335ac0    v1.0.0
acme-release-notes       sha256:a967cfa81df2    beta, v1.2.0
acme-release-notes       sha256:c4a328ab5e25    stable, v1.1.0
```

Three things to know before you talk about these numbers:

1. **The lift of v1.2.0 is still positive** (+0.33 to +0.39 over no skill). A check against the no-skill baseline alone would have promoted it. The regression gate compares its with-skill score with the stable version: 0.69 and 0.70 against 0.95, a drop of more than the 0.05 tolerance. That contrast is the best moment in the talk.
2. **Opus is an optional model, so it never gates.** Its v1.2.0 row stays `Helps` at 0.71 even though stable scored 0.95. That is the policy working as written (CONTRACT.md, "Regression gate"), not a missed regression. Point at the 0.71 and say so before someone asks.
3. **python-docstrings does not become Obsolete.** The no-skill baseline is 0.52 to 0.59, below the 0.80 Obsolete line. It is a good example of the other failure mode: the skill helps strong models and barely moves the small one (+0.03), so promotion, which needs +0.10 on every required model, stays blocked.

## Controller version check (do this first)

For the statistical gate (Inconclusive, confidence intervals) and the agent versions run controller 0.7.1 with the runner and sandbox images of the same release (`make deploy` installs all three) and apply the CRD first (`kubectl apply --server-side -f charts/skill-ci-controller/crds/`, which `make deploy` does). Older runs have no trial scores and are judged by the fixed thresholds. SkillEvals from before 0.6.0 have no `spec.agentVersion`: the controller treats them as entries without a version, and `kubectl get skillevals` shows an empty `AGENTVER` for them. Applying the pinned list of `demo/models.yaml` on top of them adds the pinned entries, so every `beta` and `stable` skill runs again; for a clean demo state run `make reset` first.

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

   Expect three reports (haiku, sonnet, opus). Only v1.1.0 has reports in Harbor: the other versions ran on a runner image that did not attach them yet, and `make report` says "No reports yet" for them. Do not demo `make report` on v1.0.0 or v1.2.0.

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

Expected: the five rows shown above. `acme-release-notes` has three digests, only v1.1.0 carries `stable`, v1.2.0 carries `beta`.

### Beat 2: a push triggers an evaluation (about 3 minutes)

Option A, the live trigger (spends LLM tokens and three Job slots, finishes in 11 to 13 minutes, so the result arrives after the talk):

```bash
demo/push-skill.sh python-docstrings v1.0.1 /tmp/live/python-docstrings
```

The audience sees, in terminals 2 and 3: the Harbor `PUSH_ARTIFACT` webhook handled with `created":3` in the controller log, then three new SkillEvals appear `Pending`, then `Running` as Jobs start (`kubectl -n skill-ci get jobs,pods` shows the runner pods). Say: the controller made one SkillEval per model on the list, for this digest. Each one is a Job that pulls the skill by digest and runs the with and without arms in sandboxes.

Do not wait for it. Say: "that takes 12 minutes, so here is one I prepared earlier" and go to beat 3.

Option B, no live trigger: show the finished object instead.

```bash
kubectl -n skill-ci get skilleval acme-release-notes-v1-0-0-claude-haiku-5-5 -o yaml
```

Point at `spec.digest`, `spec.trigger: push`, `status.withSkill: "0.95"`, `status.withoutSkill: "0.35"`, `status.lift: "+0.60"`, `status.verdict: Helps`.

Not verified: the live trigger was not run while writing this runbook (it would spend tokens and change the cluster). `demo/push-skill.sh` with a directory argument is the documented path; rehearse it once beforehand.

### Beat 3: the lift of a good skill (about 2 minutes)

```bash
kubectl -n skill-ci get skillevals -l skillci.dev/skill=acme-release-notes -o wide
```

The audience sees six rows for v1.0.0 and v1.1.0, plus the three v1.2.0 rows for the next beat. For v1.0.0: with 0.95 on all three models, without 0.35, 0.32 and 0.36, lift +0.60, +0.63 and +0.59, all `Helps`.

Say: the same skill, three models, one number each. Without the skill no model gets the house format right (about 0.35). With it, 0.95. A model that is better at general tasks does not need the skill less, because the format is arbitrary.

### Beat 4: a better version takes over `stable` (about 2 minutes)

```bash
kubectl -n skill-ci get skillevals -l skillci.dev/skill=acme-release-notes,skillci.dev/model=claude-sonnet-5-5 -o wide
make status
```

The audience sees v1.1.0 with 0.95 with-skill on every model and lift +0.58 (haiku), +0.64 (sonnet), +0.58 (opus). In the Harbor block, `stable` sits on v1.1.0 and v1.0.0 only has its version tag.

Say: v1.1.0 adds two rules (ticket-ID order, an explicit empty section). It scores as well as v1.0.0 and the lift is above +0.10 on both required models, so the controller moved `stable` to it through the Harbor API. The condition on the object says so:

```bash
kubectl -n skill-ci get skilleval acme-release-notes-v1-1-0-claude-sonnet-5-5 -o jsonpath='{.status.conditions[?(@.type=="PolicyApplied")].reason}{"\n"}'
```

Expected output: `Promoted`.

### Beat 5: the regression gate (about 4 minutes, the centrepiece)

```bash
kubectl -n skill-ci get skillevals -l skillci.dev/skill=acme-release-notes -o wide | grep v1-2-0
```

The audience sees, for v1.2.0:

```text
v1.2.0  claude-haiku-5-5    Succeeded  0.69  0.36  +0.33  Regressed
v1.2.0  claude-opus-5-5     Succeeded  0.71  0.37  +0.34  Helps
v1.2.0  claude-sonnet-5-5   Succeeded  0.70  0.31  +0.39  Regressed
```

Say: lift is still positive, so a "does it beat no skill" test passes. But stable scores 0.95, and this version scores 0.69 and 0.70. The gate compares with the version you run today, so it blocks the promotion and marks the verdict `Regressed`. Then the reason, in the object:

```bash
kubectl -n skill-ci get skilleval acme-release-notes-v1-2-0-claude-haiku-5-5 -o jsonpath='{.status.conditions[?(@.type=="Regressed")].message}{"\n"}'
```

Expected output (the stable version named is the one holding `stable` when the gate last ran, v1.1.0 once the controller has settled, v1.0.0 in an older run; both scored 0.95):

```text
claude-code/claude-haiku-5-5 scores 0.69 with v1.2.0, stable v1.1.0 scored 0.95; claude-code/claude-sonnet-5-5 scores 0.70 with v1.2.0, stable v1.1.0 scored 0.95 (tolerance 0.05; fixed rule, no trial scores to test it)
```

The pre-baked v1.2.0 runs come from a runner that did not send trial scores, so controller 0.6.0 judges them by the fixed tolerance and says so at the end of the message. Runs made by runner 0.5.1 or later carry their trial scores and the message gains the numbers (next section).

Then `make status`: v1.2.0 carries `beta` (it is the newest) but not `stable`. Point out the opus row: `Helps` at 0.71, because opus is optional and never gates. Kubernetes Events with reason `WorseThanStable` exist too, but events expire after an hour, so do not rely on `kubectl get events` for a pre-run state.

Be honest: v1.2.0 was written to fail. The real-world version of this is a well-meant edit that fixes one case and breaks two.

#### Regressed versus Inconclusive (the talking point: the gate needs evidence, not one number)

Say: the same skill version scored 0.95 on one run and 0.87 on the next. A gate that fires at "more than 0.05 below stable" would have held a good version back on that noise. So the gate does not compare two numbers. Each run keeps its 9 trial scores per arm and the controller puts a 95% confidence interval around the difference.

What the audience sees depends on the evidence. After a re-run on runner 0.5.1 or later (`make demo-push VERSION=v1.2.0`, or `make retrigger` for an existing version), with the stable version at 0.95:

```bash
kubectl -n skill-ci get skillevals -l skillci.dev/skill=acme-release-notes -o wide | grep v1-2-0
```

```text
v1.2.0  claude-haiku-5-5    Succeeded  0.70  0.36  +0.34  Regressed     ...  [+0.30, +0.38]  9/9
v1.2.0  claude-sonnet-5-5   Succeeded  0.87  0.31  +0.56  Inconclusive  ...  [+0.38, +0.74]  9/9
```

These two rows are illustrative (the numbers come from the unit-test fixtures in `controller/internal/policy`), not a measured state. Do not show them as a pre-run result unless you made the run. What the two conditions say:

- **Regressed**: `claude-code/claude-haiku-5-5 scores 0.70 with v1.2.0, stable v1.1.0 scored 0.95: difference -0.25, 95% CI [-0.26, -0.24] (tolerance 0.05)`. The whole interval is below zero: the drop is real. Warning event `WorseThanStable`.
- **Inconclusive**: `claude-code/claude-sonnet-5-5: 0.87 with v1.2.0 vs 0.95 with stable v1.1.0, difference -0.08, 95% CI [-0.18, +0.03]; not enough evidence to call it a regression`. The drop is bigger than the 0.05 tolerance, but the interval reaches above zero, so noise could explain it. Normal event `NotEnoughEvidence`.

```bash
kubectl -n skill-ci get skilleval acme-release-notes-v1-2-0-claude-sonnet-5-5 -o jsonpath='{.status.conditions[?(@.type=="Inconclusive")].message}{"\n"}'
```

Both keep `stable` where it is and give the version `beta`. The difference is what you tell the author: Regressed means fix it, Inconclusive means run more trials before deciding either way. On the reporting site the Inconclusive verdict is neutral (hollow marker, the word "Inconclusive"), and the skill page says "Not proven" with the numbers and "The stable version stays; more trials would show whether the drop is real."

The same rule works for promotion: a lift of +0.12 whose interval runs from -0.05 to +0.29 does not earn `stable`, because the skill may not help at all. The PolicyApplied message and the skill page say "not beyond noise". Set `controller.thresholds.confidence` to `"0.99"` to ask for more evidence, `"0.90"` for less.

### Beat 6: a skill that does not clear the bar (about 2 minutes)

```bash
kubectl -n skill-ci get skillevals -l skillci.dev/skill=python-docstrings -o wide
```

The audience sees: haiku 0.62 with, 0.59 without, +0.03 `Neutral`; sonnet 0.95 / 0.52 / +0.43 `Helps`; opus 0.96 / 0.55 / +0.41 `Helps`. In the Harbor block, `python-docstrings` has `beta` and no `stable`.

Say: sonnet and opus gain about 0.4, haiku scores 0.62 with the skill and 0.59 without. The rule is +0.10 on every required model, so this skill stays on `beta`. (I have not looked at why haiku gains nothing; do not claim a reason on stage.) That is a result a team would act on: improve the skill or drop haiku from the required list. Compare k8s-resource-naming (arbitrary team conventions): +0.57, +0.62, +0.57, `stable`.

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

This spends tokens and takes 11 to 13 minutes per skill version, so it is a second live trigger: start it and say "here is one I prepared earlier", then show the finished entries on the reporting site (`/skills/acme-release-notes`: one column per `agent@version` and model) or in Grafana (the `agent_version` label). Not verified end to end on the cluster: the bump was exercised with controller and runner unit tests, not with a live run.

Undo with `make demo-model-swap MODE=restore`. The runs on 2.1.308 stay; remove the entry's results with `make reset` if they should not count.

### Beat 8: other harnesses, and who gates (about 2 minutes)

```bash
kubectl -n skill-ci get skillevals -l skillci.dev/skill=acme-release-notes -o custom-columns=RUN:.metadata.name,AGENT:.spec.agent,VER:.status.agentVersion,MODEL:.spec.model,LIFT:.status.lift,REQUIRED:.spec.required
```

Say: Codex and OpenCode run the same skill with their own models. They are optional here, so a bad score does not block anything. Then flip the switch:

```bash
make demo-model-swap MODE=require AGENT=codex
```

The controller updates `spec.required` of the Codex runs and re-judges every digest: if Codex's lift is below +0.10, or its with-skill score fell against stable, the digest stops being promoted, with no new run. `MODE=optional AGENT=codex` puts it back. Not verified end to end: Codex needs a model the OpenAI key can use (the model in `demo/models.yaml` is a placeholder), and the OpenCode route through Ollama Cloud has been checked against SkillEvaluator's provider checks and the Ollama API, not with a full run.

### Beat 9: metrics and reports (about 3 minutes)

Grafana (browser tab 1, dashboard `skill-ci`): the lift matrix per skill version, `agent@version` and model, the with-skill and no-skill scores, the stable table. Say: the controller exposes `skillci_lift`, `skillci_stable`, `skillci_regressed` and friends, scraped by Prometheus. The dashboard JSON is in the repo.

Then the evidence behind one number:

```bash
make report SKILL=acme-release-notes VERSION=v1.1.0 OUT=reports/
```

The audience sees a table of three report referrers (sha256 digest, creation time, model, agent, agent version, lift +0.58, +0.64, +0.58), then three downloads, each with `report.html` and `result.json`. Open `reports/acme-release-notes-v1.1.0-claude-opus-5-5/report.html` in the browser.

Say: the full SkillEvaluator report is attached to the skill digest as an OCI referrer, so it survives the pod and travels with the artifact.

Then the closing screen, browser tab 4, the reporting site served by the controller (`make reports-ui`): the runs list with every SkillEval, the release channels, and a skill page (`/skills/acme-release-notes`) that says in words why v1.2.0 is held back and v1.1.0 is on stable. Open the run `acme-release-notes-v1-1-0-claude-haiku-5-5` for the six dimensions and the full SkillEvaluator report. Only runs from runner 0.3.0 or newer have a report; the others say "not stored". Needs controller image 0.3.0 or newer.

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
