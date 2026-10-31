# The gate

A skill version has to pass two checks before it gets `stable`. SkillEvaluator's Tier 1 and Tier 2 run first and reject unsafe or duplicate skills without spending any tokens. Then the live Tier 3 trials produce scores, and the controller decides from the confidence intervals of those scores whether the version helps and whether it is worse than the current stable version.

## Tiers and scanners in the runner

The runner evaluates every skill version through all three SkillEvaluator tiers (see [CONTRACT.md, "All tiers and publication"](../CONTRACT.md)).

1. The gate is `skillevaluator validate <skill> --tiers 1,2 --no-llm --profile external --continue-on-failure --catalog <catalog.json>`: Tier 1 static and security checks, Tier 2 deduplication. The catalog holds the other skills of the Harbor project at `stable`, else at their newest `vX.Y.Z`.
2. A failure there skips Tier 3: no sandboxes, no tokens. The controller marks the run `Rejected`, and a digest with a Rejected run on any required model never gets `beta` or `stable`, and loses `stable` if it had it.
3. Otherwise one more `validate --tiers 1,2,3` run adds the live Tier 3 evaluation, so SkillEvaluator's own report (`report.html`, `BENCHMARK.md`) carries a complete publication assessment, which the runner reads from `publication.status`.

Two patch hunks make that possible: `validate --catalog`, and the sandbox size (`SANDBOX_CPUS`, `SANDBOX_MEMORY_MB`) reaching the sandbox environment as `HARBOR_SANDBOX_CPUS` and `HARBOR_SANDBOX_MEMORY_MB`, because `validate` has no `--override-cpus`.

Tier 1 is only trustworthy with its scanners, so the runner image pins them: Gitleaks 8.30.1 (binary, sha256-checked), SkillSpector v2.12.0 and Semgrep 1.183.0 (each in its own virtualenv, because they pin conflicting click versions), Bandit 1.9.4 and pip-audit 2.10.1 (SkillEvaluator's `security` extra). Without them SkillEvaluator marks Tier 1 `incomplete`. Tier 2 needs an embeddings provider, and Anthropic has none: put an `openai` key into the Secret `llm-keys`, or Tier 2 reports `incomplete`.

The profile is SkillEvaluator's built-in `external`, the only one it ships. For skills published to an internal platform it asks for nothing the sample skills lack (a `Name <email>` author, a permissive license, no blocked licenses) and keeps license findings blocking. Override it with `TIER1_PROFILE` on the runner.

`demo/variants/k8s-resource-naming/v1.1.0` is a deliberately unsafe variant that Tier 1 rejects, see [demo/variants/README.md](../demo/variants/README.md). To run the checks locally, install the same scanners and run `skillevaluator validate skills/<name> --tiers 1,2`. The skill directory must be named like the skill.

## How the gate decides

A fixed number is a bad gate for a noisy measurement. The same skill version scored 0.95 on one run and 0.87 on the next, and a rule of "more than 0.05 below stable is a regression" held a good version back. Evidence-based evaluation avoids that: SWE-Skills-Bench, for one, found that 92 of 98 skills had no statistically significant effect. The skill-ci gate asks for evidence too.

Each run has 3 eval cases x 3 attempts per arm, so up to 9 trial scores with the skill and 9 without. The runner sends them (`samples` in the termination message), and the controller computes a confidence interval (Welch's t, 95% by default) and decides:

| Question | Rule |
| --- | --- |
| Does the skill help? | Lift is at least +0.10 and the lift interval is entirely above 0, on every required model. Otherwise the version is not promoted. |
| Did the new version get worse than stable? | With-skill score is more than 0.05 below stable on a required model and the interval of the difference is entirely below 0: `Regressed`. |
| Is the drop only maybe real? | More than 0.05 below, but the interval includes 0: `Inconclusive`. Not promoted, stable stays, `beta` is kept, and the numbers are in the condition. |
| Do older runs still work? | A run without trial scores (runner before 0.5.1) uses the fixed thresholds above and says so in its condition. |

`--confidence` (Helm `controller.thresholds.confidence`, default 0.95) sets the level. A higher value asks for more evidence both to promote and to call a regression. A worked example, with stable `v1.0.0` at 0.95 on a required model:

- `v1.2.0` scores 0.70 with tight trials: difference -0.25, 95% CI [-0.26, -0.24], `Regressed`.
- `v1.2.0` scores 0.87 with spread-out trials: difference -0.08, 95% CI [-0.18, +0.03], `Inconclusive`. The drop is bigger than the tolerance, but noise could explain it.

The formulas and the exact rules are in [CONTRACT.md](../CONTRACT.md), "Statistics" and "Policy".

## Policy in short

Per SkillEval, the lift over the no-skill run gives the verdict: `Helps` from +0.05, `Hurts` at -0.10 or lower, each only when the lift interval clears 0, and `Obsolete` when a neutral skill has a no-skill score of 0.80 or more. Per skill digest, once every required model has a result:

- The newest evaluated digest gets `beta`.
- A digest with a lift of at least +0.10 on every required model (interval above 0) gets `stable`, unless it fails the regression gate: its with-skill score on a required model is more than 0.05 below the current stable digest's score and the interval of that difference is below 0. Then it keeps `beta`, its SkillEvals get a `Regressed` condition (with the models, scores and interval in the message) and the verdict `Regressed` on the models that dropped. When the interval includes 0 the verdict is `Inconclusive` instead, with the same consequences for the tags. Optional models never gate.
- An older version does not take `stable` from a newer one unless the newer one scores worse than it.
- A digest loses `stable` when its lift is -0.10 or lower on a required model with the interval below 0.
- Verdicts depend on other digests, so when a SkillEval succeeds or the policy changes a tag, the controller re-runs the policy for the other finished SkillEvals of the same skill.

## Thresholds

Every threshold is a controller flag and a Helm value:

| Helm value | Flag | Default |
| --- | --- | --- |
| `controller.thresholds.helps` | `--helps-threshold` | `0.05` |
| `controller.thresholds.hurts` | `--hurts-threshold` | `-0.10` |
| `controller.thresholds.promote` | `--promote-threshold` | `0.10` |
| `controller.thresholds.demote` | `--demote-threshold` | `-0.10` |
| `controller.thresholds.obsoleteBaseline` | `--obsolete-baseline` | `0.80` |
| `controller.thresholds.regressionTolerance` | `--regression-tolerance` | `0.05` |
| `controller.thresholds.confidence` | `--confidence` | `0.95` |
| `controller.defaultAttempts` | `--default-attempts` | `3` |

The details are in [CONTRACT.md, "Policy"](../CONTRACT.md) and the [chart README](../charts/skill-ci-controller/README.md).
