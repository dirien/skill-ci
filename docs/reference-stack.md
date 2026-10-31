# Reference stack on DOKS

[infra/Pulumi.yaml](../infra/Pulumi.yaml) is a Pulumi YAML program that builds a complete skill-ci environment on DigitalOcean Kubernetes (DOKS). It is one way to get a cluster, and the one the end-to-end checks in [design-notes.md](design-notes.md#verified-end-to-end) ran on. skill-ci itself runs on any conformant cluster; for other clusters follow [deploy/README.md](../deploy/README.md), which installs the same components with Helm and kubectl.

## What the stack creates

- A DOKS cluster on the newest 1.36 patch release, with an autoscaled node pool of 2 to 5 `s-4vcpu-8gb` nodes (region `fra1` by default).
- Agent Sandbox v1.0.9, goharbor (chart 1.19.4), the Secrets, the Harbor setup Job, the toolbox, the model list, and Prometheus and Grafana (kube-prometheus-stack chart 92.10.1).
- The skill-ci controller from the published OCI chart of the stack's `skillCiVersion` (default 0.10.1), with the CRDs applied separately because Helm never upgrades `crds/`.

The versions are stack config with the same defaults as [deploy/versions.env](../deploy/versions.env); `deploy/check-versions.sh` compares the two files.

## Running it

```bash
make up                       # or: cd infra && pulumi up
make deploy                   # the stack's skillCiVersion, or TAG=0.8.1
kubectl -n skill-ci create secret generic llm-keys --from-literal=anthropic="$ANTHROPIC_API_KEY"
make seed RETRIGGER=1
make status
```

`make up` runs `pulumi up` for the cluster, Harbor, monitoring, the toolbox and the controller of the stack's release. `make preview` shows what it would change. `make deploy TAG=...` later moves the stack to another release. The retrigger in `make seed RETRIGGER=1` matters when the skills were pushed before the controller existed, because Harbor retries a failed webhook delivery only a few times.

The Makefile sources `PULUMI_ENV` (default `$(HOME)/.pulumi-local/skill-ci-env.sh`) before each Pulumi command when that file exists, so you can keep the backend and credentials for the stack there.

`make down` refuses on purpose. Destroying the stack deletes the cluster, Harbor and the registry, so run it by hand:

```bash
cd infra && pulumi destroy
```

## Infrastructure notes

- Harbor has `externalURL: http://harbor.harbor.svc.cluster.local`, so pods can reach its token service. From outside the cluster, reach it only through in-cluster tooling: the toolbox pod or `kubectl -n harbor port-forward svc/harbor`.
- The setup Job `harbor-setup-<id>` waits for Harbor, creates the private project `skills` if it is missing, and creates or updates the webhook policy `skill-ci` (`PUSH_ARTIFACT`, payload `Default`, `Authorization: Bearer <harbor-webhook token>`) pointing at the controller Service. It also removes policies that still point at an old debug receiver. The script is `infra/harbor-setup.sh`, shared with the install in `deploy/`. The Job name carries a random id that changes when the script, the image, the admin password or the token change, so every relevant `pulumi up` runs a fresh, idempotent Job.
- The Secrets in `skill-ci` are `harbor-credentials` (user `admin` and the generated admin password), `harbor-pull` (Docker config for `harbor.harbor.svc.cluster.local`, same credentials) and `harbor-webhook` (random token). A project-scoped Harbor robot account is the production choice; admin keeps the stack reproducible from one `pulumi up`. `llm-keys` is not managed by Pulumi. Create it by hand as shown above and never commit key values, see [agents-and-models.md](agents-and-models.md) for the keys.
- The Deployment `toolbox` has two containers: `oras` (pinned image, the `harbor-pull` secret at `/auth/config.json`) and `harbor-api` (curl with the admin password in its env). The scripts in `demo/` copy a skill directory in with `tar` over `kubectl exec` and run `oras push --plain-http --registry-config /auth/config.json`.
- The ConfigMap `skill-ci-models` is seeded from `demo/models.yaml` and has `ignoreChanges: [data]` in Pulumi, so edits survive later `pulumi up` runs. `make reset` restores it from the same file.
- kube-prometheus-stack runs in the namespace `monitoring`: Prometheus (24h retention) and Grafana, with Alertmanager, node exporter, kube-state-metrics, default rules and the control-plane scrape jobs switched off. A `ServiceMonitor` in `skill-ci` scrapes Service `skill-ci-controller` (port `metrics`, label `app.kubernetes.io/name=skill-ci-controller`), and the dashboard `infra/dashboards/skill-ci.json` is provisioned through a ConfigMap. The Grafana admin password is generated and exported as the secret stack output `grafanaAdminPassword`.
- The OTLP export is stack config too: `pulumi config set otlpEndpoint ...` (see `infra/Pulumi.yaml`).

## Cost

There is no registry or builder to run: the images are public GHCR packages. Prometheus and Grafana run on the two nodes the pool starts with (requests of about 0.2 vCPU and 0.6 GiB in total). Live evaluations cost LLM tokens; the cost guard flags are in [CONTRACT.md, "Cost guard"](../CONTRACT.md) and summarized in [network-and-sandboxes.md](network-and-sandboxes.md#sandbox-sizing-and-concurrency).
