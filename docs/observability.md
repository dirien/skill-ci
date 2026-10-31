# Results, metrics and the reporting site

skill-ci shows results in four places: the `SkillEval` objects themselves, a read-only reporting site served by the controller, Prometheus metrics (with an optional OTLP push), and the SkillEvaluator reports stored next to each skill in Harbor.

## SkillEvals

```bash
kubectl -n skill-ci get skillevals -o wide
```

The printer columns show skill, version, agent and agent version, model, phase, the scores with and without the skill, the lift, the verdict, the Tier 1 result and the network mode. `-o wide` adds the lift interval, the number of trials, the publication status and the report digest. `make status` prints the same list plus the tags and digests per skill from the Harbor API.

## Reporting site

The controller serves a read-only reporting site on port 8082 (`--reports-addr`, empty disables it; Helm `reports.enabled`). `make reports-ui` port-forwards it to <http://localhost:8082>.

- `/` shows the network policy in force and lists every run (SkillEval) newest first, with release channels per skill and filters (`?skill=`, `?agent=claude-code@2.1.292`, `?model=`, `?verdict=`, `?phase=`, `?publication=`).
- `/skills/<skill>` shows versions against model list entries (one column per `agent@version` and model) with the lift (and its confidence interval and n), the channel tags and the policy decision per version in plain words.
- `/runs/<name>` shows one run: scores with the lift interval and n, the SkillEvaluator tiers and findings, the sandbox and the network of the run, conditions and, when a report is stored, the six SkillEvaluator dimensions with and without the skill. The full SkillEvaluator report and `result.json` open from there.

Reports are read from Harbor as OCI referrers of the evaluated skill digest and cached in memory. Runs from runners before 0.3.0 stored none and show "not stored". The pages need no internet, apart from the full SkillEvaluator report, which loads Chart.js from a CDN. `make report-page` builds an offline page with every SkillEval and the SkillEvaluator reports (`OUT=reports/index.html`). The routes are listed in [CONTRACT.md, "Reporting site"](../CONTRACT.md).

## Eval reports in Harbor

`make report` downloads the eval reports attached to a skill version in Harbor, the newest per `agent@version/model` entry:

```bash
make report SKILL=acme-release-notes VERSION=v1.1.0 OUT=reports/
```

`MODEL=...` limits it to one model. Each report holds `report.html`, `result.json` and, when SkillEvaluator wrote one, `BENCHMARK.md`.

## Prometheus metrics

The controller serves its metrics on port 8081 at `/metrics`. Every per-run series has the labels `skill`, `version`, `agent`, `agent_version` and `model`. The main families:

| Metric | Meaning |
| --- | --- |
| `skillci_lift`, `skillci_with_score`, `skillci_without_score` | Scores of a run |
| `skillci_lift_ci_lower`, `skillci_lift_ci_upper` | The lift interval at `--confidence`, for runs with trial scores |
| `skillci_stable`, `skillci_obsolete`, `skillci_regressed`, `skillci_inconclusive`, `skillci_rejected` | 1 when a skill version is in that state |
| `skillci_publication` | 1 for the SkillEvaluator publication status of a run |
| `skillci_skillevals_total`, `skillci_skillevals` | Phase transitions, and the current number of SkillEvals per phase |
| `skillci_retries_total` | Runs started again after an infrastructure failure, by reason |

The full list with all labels is in [CONTRACT.md, "Metrics"](../CONTRACT.md). When a SkillEval is deleted the controller removes its series, so dashboards never show the numbers of a deleted run.

With the Prometheus Operator installed, set `serviceMonitor.enabled=true` in the chart. The ServiceMonitor selects Service `skill-ci-controller` by `app.kubernetes.io/name: skill-ci-controller`, port `metrics`. For kube-prometheus-stack set `serviceMonitor.labels.release`. The install in [deploy/README.md](../deploy/README.md) adds a ServiceMonitor of its own in `deploy/manifests/monitoring`; do not enable both.

## Grafana dashboard

`infra/dashboards/skill-ci.json` is the dashboard, delivered as a ConfigMap with the `grafana_dashboard` label that Grafana's sidecar loads. It shows the lift matrix per skill version, `agent@version` and model (`skillci_lift`), the lift with its confidence interval (`skillci_lift_ci_lower`, `skillci_lift_ci_upper`), scores with and without the skill, `skillci_stable`, `skillci_obsolete`, the Inconclusive and Regressed counts and `skillci_skillevals_total`. `make dashboard` port-forwards Grafana to `localhost:3000` and prints the login hint.

## OpenTelemetry export

The controller can also push the same `skillci_*` metrics over OTLP to an OpenTelemetry Collector, Grafana Alloy, the Prometheus OTLP receiver or a vendor endpoint. It is off by default and turned on by the Helm value `otlp.endpoint`:

```bash
# An OpenTelemetry Collector or Grafana Alloy in the cluster
--set otlp.endpoint=otel-collector.monitoring:4318 --set otlp.insecure=true
```

Only the `skillci_*` families are exported, with the same names, labels and values as `/metrics`. Export errors are logged and never stop the controller. An API or ingest key goes into a Secret named by `otlp.headersSecret.name`, never on the command line.

Do not scrape and push into the same Prometheus: every series would be stored twice and the dashboard would sum both. Keep the ServiceMonitor or point the OTLP export at Prometheus, not both.

The chart values are in the [chart README](../charts/skill-ci-controller/README.md), the `install.sh` variables in [deploy/README.md](../deploy/README.md), and the flags and resource attributes in [CONTRACT.md, "OpenTelemetry export"](../CONTRACT.md). On the Pulumi reference stack the export is stack config: `pulumi config set otlpEndpoint ...` (see `infra/Pulumi.yaml`).
