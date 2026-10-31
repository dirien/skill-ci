# Run skill-ci on your own cluster

This is the follow-along path: plain Helm charts and Kubernetes manifests, no Pulumi, no cloud account beyond the
cluster itself. You end up with the same setup as the demo: skills pushed to Harbor are evaluated by a controller, and
the results decide which version gets the `beta` and `stable` tags.

All images come from GHCR and need no pull secret, and nothing builds images in the cluster.

| Image or chart | Reference |
| --- | --- |
| Controller | `ghcr.io/dirien/skill-ci-controller:<version>` |
| Runner (one Job per evaluation) | `ghcr.io/dirien/skill-ci-runner:<version>` |
| Trial sandbox (Claude Code, Codex and OpenCode preinstalled) | `ghcr.io/dirien/skill-ci-sandbox:<version>` |
| Controller chart | `oci://ghcr.io/dirien/charts/skill-ci-controller` |

## What gets installed

| Component | Where | How |
| --- | --- | --- |
| [Agent Sandbox](https://github.com/kubernetes-sigs/agent-sandbox) (CRDs and controller) | `agent-sandbox-system` | release manifest `sandbox-with-extensions.yaml` |
| Harbor | `harbor` | Helm chart `harbor` from `https://helm.goharbor.io` |
| Namespace, secrets, model list, toolbox | `skill-ci` | `kubectl` and `deploy/manifests` |
| Harbor project `skills` and its webhook policy | in Harbor | a Job (`deploy/manifests/harbor-setup`) |
| Prometheus and Grafana (optional) | `monitoring` | Helm chart `kube-prometheus-stack`, plus a ServiceMonitor and a dashboard |
| skill-ci controller, `SkillEval` CRD, runner RBAC | `skill-ci` | OCI Helm chart |

## Prerequisites

- A Kubernetes cluster, version 1.30 or newer, with a default StorageClass that provisions `ReadWriteOnce` volumes.
  Harbor (database, registry, jobservice, redis) and Prometheus each need one.
- About three nodes of 4 vCPU and 8 GiB, with node autoscaling if you can. Every evaluation is a runner Job plus several
  sandbox pods of 1 vCPU and 2 GiB each, and up to three evaluations run at once.
  A smaller cluster works if you lower `controller.maxConcurrentJobs` (see [Platform notes](#platform-notes)).
- `kubectl` and `helm` (3.17 or 4) on your machine, with `KUBECONFIG` pointing at the cluster.
- `git`, `make`, `python3` and `tar` for the demo scripts. `oras` is not needed on your machine: the demo pushes skills
  from a toolbox pod inside the cluster, because Harbor is only reachable there.
- An Anthropic API key. It is the judge of every run. Keys for OpenAI, OpenRouter, Ollama Cloud and NVIDIA are optional
  (they add Codex and OpenCode models).
- Outbound internet access from the nodes: GHCR, `github.com`, `helm.goharbor.io`, `prometheus-community.github.io`, the LLM APIs.
- For the network modes `none` and `allowlist`: Cilium as the CNI (DOKS has it; check with
  `kubectl get crd ciliumnetworkpolicies.cilium.io`). Without Cilium every skill runs as `full` (the internet, never the
  cluster, through Agent Sandbox's own policy), and a skill that asks for `none` or `allowlist` fails with a message
  that says so. See [Network modes](#network-modes-and-sandbox-profiles).

Clone the repository (the demo scripts and the sample skills live there) and read the versions once:

```bash
git clone https://github.com/dirien/skill-ci.git
cd skill-ci
source deploy/versions.env
kubectl version
kubectl get storageclass
```

Expected: a server version of v1.30 or newer, and one StorageClass marked `(default)`.

```text
NAME                         PROVISIONER                 RECLAIMPOLICY   VOLUMEBINDINGMODE   ...
do-block-storage (default)   dobs.csi.digitalocean.com   Delete          Immediate           ...
```

## The quick way

If you only want it running:

```bash
export ANTHROPIC_API_KEY=sk-ant-...      # required
export OPENAI_API_KEY=...                # optional: Codex
export OPENROUTER_API_KEY=...            # optional: OpenCode with openrouter/... models
deploy/install.sh
```

The script runs the steps below in order, prints each one, and can be run again: it keeps the generated passwords,
upgrades what changed and never overwrites the model list. A first install takes about 20 minutes (most of it Harbor
and kube-prometheus-stack becoming ready); a re-run with nothing to change takes about a minute. Harbor is upgraded
only when its chart version or `deploy/helm/values/harbor.yaml` changed, because the chart generates new internal
secrets on every render and each upgrade restarts Harbor's core, jobservice and registry pods (`HARBOR_UPGRADE=1`
upgrades it anyway). `SKIP_MONITORING=1` leaves out Prometheus and Grafana. To also push the controller's metrics
over OpenTelemetry, set `OTLP_ENDPOINT` and the other `OTLP_*` variables of
[OpenTelemetry export](#opentelemetry-export-optional) before the run, and on every later run.
Then go to [Run the demo](#run-the-demo). Read on for what each step does and how to check it.

## Step 1: namespace

```bash
kubectl apply -f deploy/manifests/namespaces.yaml
kubectl get namespace skill-ci
```

Expected:

```text
NAME       STATUS   AGE
skill-ci   Active   2s
```

## Step 2: Agent Sandbox

Every trial runs in a `Sandbox` (a custom resource) that the Agent Sandbox controller turns into a pod. The release
manifest includes the extensions (`SandboxClaim`, `SandboxTemplate`, `SandboxWarmPool`) that the runner uses.

```bash
kubectl apply --server-side -f "https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${AGENT_SANDBOX_VERSION}/sandbox-with-extensions.yaml"
kubectl -n agent-sandbox-system rollout status deployment/agent-sandbox-controller
kubectl get crd sandboxes.agents.x-k8s.io
```

Expected:

```text
deployment "agent-sandbox-controller" successfully rolled out
NAME                       CREATED AT
sandboxes.agents.x-k8s.io  2026-10-30T14:05:32Z
```

## Step 3: secrets

Five Secrets, none of them stored in this repository. Templates with placeholders are in
[`deploy/manifests/secrets/`](manifests/secrets/). The commands below create the real ones.

| Secret | Namespace | Used by | Where the value comes from |
| --- | --- | --- | --- |
| `harbor-credentials` | `skill-ci` | controller, toolbox, setup Job (Harbor REST API) | You choose the Harbor admin password; Harbor is installed with the same one in step 4. The demo uses `admin`; a project robot account is the production choice. |
| `harbor-pull` | `skill-ci` | runner Jobs and toolbox (`oras` pull and push) | The same login as a registry config for `harbor.harbor.svc.cluster.local`. |
| `harbor-webhook` | `skill-ci` | controller and the Harbor webhook policy | A random token you generate. The setup Job gives it to Harbor, the controller checks it on every webhook. |
| `llm-keys` | `skill-ci` | runner Jobs | Your provider accounts. Only `anthropic` is required. |
| `grafana-admin` | `monitoring` | Grafana | You choose the password. Only needed with monitoring. |

The keys inside `llm-keys`:

| Key | Needed for | Get it from |
| --- | --- | --- |
| `anthropic` | required: the judge, Claude Code, OpenCode `anthropic/...` models | [console.anthropic.com](https://console.anthropic.com) |
| `openai` | Codex (`openai/...` models), Tier 2 embeddings | [platform.openai.com](https://platform.openai.com) |
| `openai-base-url` | Codex against a compatible endpoint; defaults to `https://api.openai.com/v1` | your provider |
| `openrouter` | OpenCode `openrouter/...` models (the demo model list uses one). An API key from openrouter.ai/settings/keys, not a provisioning key: those cannot run models, and the runner fails the run when it gets one | [openrouter.ai](https://openrouter.ai) |
| `ollama` | OpenCode `ollama-cloud/...` models | [ollama.com](https://ollama.com) |
| `nvidia` | OpenCode `nvidia/...` models | [build.nvidia.com](https://build.nvidia.com) |

Generate the passwords and keep them in your shell (or a password manager). If you lose the Harbor password, read it
back from the Secret.

```bash
export HARBOR_ADMIN_PASSWORD="Hb1$(openssl rand -hex 10)"
export WEBHOOK_TOKEN="$(openssl rand -hex 20)"
export ANTHROPIC_API_KEY=sk-ant-...

kubectl -n skill-ci create secret generic harbor-credentials \
  --from-literal=username=admin --from-literal=password="${HARBOR_ADMIN_PASSWORD}"
kubectl -n skill-ci create secret docker-registry harbor-pull \
  --docker-server=harbor.harbor.svc.cluster.local --docker-username=admin --docker-password="${HARBOR_ADMIN_PASSWORD}"
kubectl -n skill-ci create secret generic harbor-webhook --from-literal=token="${WEBHOOK_TOKEN}"
kubectl -n skill-ci create secret generic llm-keys \
  --from-literal=anthropic="${ANTHROPIC_API_KEY}"
# Optional keys: add them to the command above, for example
#   --from-literal=openai="${OPENAI_API_KEY}" --from-literal=openrouter="${OPENROUTER_API_KEY}"

kubectl apply -f deploy/manifests/monitoring/namespace.yaml
kubectl -n monitoring create secret generic grafana-admin \
  --from-literal=admin-user=admin --from-literal=admin-password="$(openssl rand -hex 12)"
```

`--from-literal` puts the value in your shell history and briefly in the process list. `deploy/install.sh` passes
values through file descriptors instead.

Verify:

```bash
kubectl -n skill-ci get secrets
```

Expected:

```text
NAME                 TYPE                             DATA   AGE
harbor-credentials   Opaque                           2      5s
harbor-pull          kubernetes.io/dockerconfigjson   1      5s
harbor-webhook       Opaque                           1      5s
llm-keys             Opaque                           1      5s
```

## Step 4: Harbor

Harbor stores the skills as OCI artifacts and calls the controller's webhook on every push. The values file
[`helm/values/harbor.yaml`](helm/values/harbor.yaml) fixes three things you should know about:

- `externalURL: http://harbor.harbor.svc.cluster.local`. Pods authenticate against Harbor's token service at this URL,
  so it has to resolve inside the cluster. It also means Harbor is not reachable from your laptop, which is why the
  demo works through a toolbox pod. There is no ingress and no TLS on purpose.
- `updateStrategy: Recreate`. Block storage is `ReadWriteOnce`: a rolling update starts the new pod while the old one
  still holds the volume, and the rollout deadlocks.
- `safe-to-evict: "false"` on every component, so the cluster autoscaler does not move Harbor during a run.

```bash
helm upgrade --install harbor harbor \
  --repo https://helm.goharbor.io --version "${HARBOR_CHART_VERSION}" \
  --namespace harbor --create-namespace \
  -f deploy/helm/values/harbor.yaml \
  --set-string harborAdminPassword="${HARBOR_ADMIN_PASSWORD}" \
  --wait --timeout 15m
kubectl -n harbor get pods
```

Harbor only reads the admin password when its database is first created. Changing it later in Helm does not change
the login, so keep `harbor-credentials` and this value identical.

Every `helm upgrade` of this chart restarts `harbor-core`, `harbor-jobservice` and `harbor-registry`, even with the
same values: the chart generates its internal secrets on each render, and their checksums are pod annotations. Skills
cannot be pushed or pulled during that minute, so upgrade Harbor between eval runs, not during one.

Expected after a few minutes (names carry random suffixes):

```text
NAME                                 READY   STATUS    RESTARTS   AGE
harbor-core-6b8bcc9777-xq5ph         1/1     Running   0          3m
harbor-database-0                    1/1     Running   0          3m
harbor-jobservice-69896fc767-r8d7l   1/1     Running   0          3m
harbor-nginx-78d7c55444-vgvdl        1/1     Running   0          3m
harbor-portal-557cbfdb7-2dwmd        1/1     Running   0          3m
harbor-redis-0                       1/1     Running   0          3m
harbor-registry-f96dbf797-kbp5l      2/2     Running   0          3m
```

`harbor-core` and `harbor-jobservice` may restart once or twice while the database starts. That is normal.

## Step 5: monitoring (optional)

Optional (`SKIP_MONITORING=1` leaves it out of `install.sh`). The controller works without it; you lose the Grafana
dashboard and metrics. Skip it on a small cluster.

```bash
helm upgrade --install kps kube-prometheus-stack \
  --repo https://prometheus-community.github.io/helm-charts --version "${KUBE_PROMETHEUS_STACK_VERSION}" \
  --namespace monitoring \
  -f deploy/helm/values/kube-prometheus-stack.yaml \
  --wait --timeout 10m
kubectl apply -k deploy/manifests/monitoring
kubectl -n monitoring get pods
```

The stack is trimmed to Prometheus, the operator and Grafana. `deploy/manifests/monitoring` adds the `ServiceMonitor`
that scrapes the controller's `/metrics` and the dashboard ConfigMap that Grafana's sidecar loads (label
`grafana_dashboard`). Both are generated or checked by `deploy/generate.sh`, see [Versions](#versions-and-generated-files).

Expected:

```text
NAME                                                  READY   STATUS    RESTARTS   AGE
kps-grafana-59fff7fcb9-qb8nz                          3/3     Running   0          2m
kps-kube-prometheus-stack-operator-57b769b598-gfrkr   1/1     Running   0          2m
prometheus-kps-kube-prometheus-stack-prometheus-0     2/2     Running   0          2m
```

## Step 6: toolbox

The toolbox pod holds `oras` and `curl`. The demo scripts run `kubectl exec` into it to push skills and call Harbor's
API from inside the cluster.

```bash
kubectl apply -k deploy/manifests
kubectl -n skill-ci rollout status deployment/toolbox
```

Expected:

```text
namespace/skill-ci unchanged
deployment.apps/toolbox created
deployment "toolbox" successfully rolled out
```

## Step 7: model list

The model list says which agents, agent versions and models every skill is evaluated with, and which of them gate
promotion (`required: true`). It is a ConfigMap you can edit while the controller runs, which is what
`make demo-model-swap` does. Create it once and never re-apply it, or you overwrite your edits.

```bash
kubectl create -f deploy/manifests/seed/skill-ci-models.yaml
kubectl -n skill-ci get configmap skill-ci-models
```

The content comes from [`demo/models.yaml`](../demo/models.yaml) (`deploy/generate.sh` writes the manifest). It has
three entries: Claude Code with an Anthropic model (`required: true`, the gate), Codex with an OpenAI model and
OpenCode with a Qwen model through OpenRouter (both `required: false`). The optional entries need the `openai` and
`openrouter` keys. An optional entry whose key is missing fails that run before any work and, being optional, never
gates, so with only an Anthropic key the Claude Code entry still decides.

## Step 8: the controller

```bash
helm show crds oci://ghcr.io/dirien/charts/skill-ci-controller --version "${SKILL_CI_VERSION}" | kubectl apply --server-side -f -
helm upgrade --install skill-ci oci://ghcr.io/dirien/charts/skill-ci-controller \
  --version "${SKILL_CI_VERSION}" \
  --namespace skill-ci \
  -f deploy/helm/values/skill-ci-controller.yaml \
  --set-string image.tag="${SKILL_CI_VERSION}" \
  --set-string controller.runnerImage.tag="${SKILL_CI_VERSION}" \
  --set-string controller.sandboxImage.tag="${SKILL_CI_VERSION}" \
  --wait --timeout 5m
kubectl -n skill-ci get deployment skill-ci-controller
```

The first command applies the `SkillEval` CRD. Helm installs a chart's `crds/` once and never upgrades it, so apply the
CRD yourself on every upgrade.

Expected:

```text
NAME                  READY   UP-TO-DATE   AVAILABLE   AGE
skill-ci-controller   1/1     1            1           30s
```

Check that the controller found its Secrets and ConfigMap:

```bash
kubectl -n skill-ci logs deployment/skill-ci-controller --tail=5
```

You should see the manager starting and the webhook server listening on `:8080` without errors about `harbor-credentials`
or `skill-ci-models`.

### OpenTelemetry export (optional)

The controller can also push its `skillci_*` metrics over OTLP to an OpenTelemetry Collector, Grafana Alloy, a
Prometheus OTLP receiver or a vendor endpoint. It is off by default. `install.sh` reads these variables and passes
each one to the chart only when it is set (the Pulumi path uses stack config instead, `otlp*` in `infra/Pulumi.yaml`):

| Variable | Chart value | Example |
| --- | --- | --- |
| `OTLP_ENDPOINT` | `otlp.endpoint` (turns the export on) | `otel-collector.monitoring:4318` |
| `OTLP_PROTOCOL` | `otlp.protocol` | `http/protobuf` (default) or `grpc` |
| `OTLP_URL_PATH` | `otlp.urlPath` | `/api/v1/otlp/v1/metrics` for the Prometheus OTLP receiver |
| `OTLP_INSECURE` | `otlp.insecure` | `true` for a receiver inside the cluster without TLS |
| `OTLP_INTERVAL` | `otlp.interval` | `30s` (default 60s) |
| `OTLP_HEADERS_SECRET` | `otlp.headersSecret.name` | `otlp-headers` |

```bash
OTLP_ENDPOINT=otel-collector.monitoring:4318 OTLP_INSECURE=true deploy/install.sh
```

By hand, add the same values to the `helm upgrade` above, for example
`--set-string otlp.endpoint=otel-collector.monitoring:4318 --set otlp.insecure=true`. Helm does not keep values
between upgrades here, so pass them on every run: a run without them turns the export off again.

An API or ingest key goes into a Secret in `skill-ci`, never on the command line. The chart reads its key `headers`
(comma separated `key=value` pairs) into `OTEL_EXPORTER_OTLP_HEADERS`:

```bash
kubectl -n skill-ci create secret generic otlp-headers --from-literal=headers='x-honeycomb-team=<ingest key>'
OTLP_ENDPOINT=api.honeycomb.io:443 OTLP_PROTOCOL=grpc OTLP_HEADERS_SECRET=otlp-headers deploy/install.sh
```

`install.sh` stops before installing anything when that Secret does not exist.

Do not scrape and push into the same Prometheus. With monitoring installed, the ServiceMonitor already scrapes the
controller into `kps-kube-prometheus-stack-prometheus`; pushing there as well stores every series twice and the
dashboard sums both. Point the export at another backend. The metric names and labels are in
[CONTRACT.md, "OpenTelemetry export"](../CONTRACT.md#opentelemetry-export-optional-otlp).

## Step 9: Harbor project and webhook

A one-time Job waits for Harbor, creates the private project `skills` and a webhook policy on it that sends
`PUSH_ARTIFACT` events to the controller with the bearer token from `harbor-webhook`. It runs the script
[`infra/harbor-setup.sh`](../infra/harbor-setup.sh) from a ConfigMap, so the Pulumi path and this one share it.
A Job's pod template is immutable, so delete the Job before running it again.

```bash
kubectl -n skill-ci delete job harbor-setup --ignore-not-found
kubectl apply -k deploy/manifests/harbor-setup
kubectl -n skill-ci wait --for=condition=complete job/harbor-setup --timeout=10m
kubectl -n skill-ci logs job/harbor-setup
```

Expected (the end of the log, abridged):

```text
Harbor is healthy.
Creating private project skills ...
Project skills has id 2.
Creating webhook policy skill-ci ...
Webhook policies on skills now:
"event_types":["PUSH_ARTIFACT"]
"address":"http://skill-ci-controller.skill-ci.svc.cluster.local:8080/webhook/harbor"
Done.
```

## Step 10: check the whole system

```bash
kubectl get pods -A | grep -v 'kube-system'
kubectl -n skill-ci get servicemonitor,skillevals
```

Everything should be `Running` or `Completed`, and `kubectl get skillevals` returns "No resources found" until you
push a skill. If you skipped monitoring, there is no ServiceMonitor.

## Run the demo

The demo scripts in [`demo/`](../demo/) talk to the cluster with `kubectl` only, so the same `make` targets work here.
Run them from the repository root with `KUBECONFIG` exported. The Pulumi targets (`make up`, `make preview`,
`make down`) and `make deploy` are replaced by this guide.

```bash
make seed                    # push the three sample skills as v1.0.0 and start their first evaluations
make status                  # SkillEvals and the tags per skill in Harbor
kubectl -n skill-ci get skillevals -o wide --watch
```

Expected: each `SkillEval` goes `Pending`, `Running`, `Succeeded` over a few minutes, with `With`, `Without`, `Lift` and a
verdict filled in, and `make status` shows the `beta` and `stable` tags appearing on skills that earn them. A first run
is slower because every node pulls the sandbox image once.

### Skills need test cases

The live evaluation (Tier 3) runs the test cases in a skill's `evals/evals.json`; the sample skills ship with one. A
skill pushed without it is checked by Tier 1 and Tier 2 only and then fails with
`no eval dataset: the live evaluation (Tier 3) needs test cases in evals/evals.json. ...`. Draft the cases before
you push:

```bash
make draft-evals SKILL=my-skill                  # writes skills/my-skill/evals/evals.json
make draft-evals SKILL=my-skill DIR=path/to/my-skill FORCE=1   # another directory; replace an existing file
```

It runs SkillEvaluator once in a short-lived pod from the runner image, with the `anthropic` key from `llm-keys`:
one LLM request, about 20 seconds. The draft is a starting point, not a gate. Read and fix the cases before you push
the skill.

| Target | What it does |
| --- | --- |
| `make seed` | Pushes every skill under `skills/` as `v1.0.0` |
| `make demo-push SKILL=acme-release-notes VERSION=v1.1.0` | Pushes a new version (`v1.2.0` is a deliberate regression) |
| `make draft-evals SKILL=...` | Drafts `evals/evals.json` for a skill, for you to review |
| `make demo-model-swap MODE=add` | Changes the model list live (`add`, `swap`, `restore`, `bump`, `require`, `optional`) |
| `make status` | SkillEvals and the tags per skill |
| `make report SKILL=... VERSION=... OUT=reports/` | Downloads the eval reports stored in Harbor |
| `make dashboard` | Port-forwards Grafana to `localhost:3000` (user `admin`) |
| `make reports-ui` | Port-forwards the controller's reporting site |
| `make reset` | Back to the start: deletes SkillEvals, resets tags, restores the model list |

Grafana's password:

```bash
kubectl -n monitoring get secret grafana-admin -o jsonpath='{.data.admin-password}' | base64 --decode; echo
```

`make dashboard` prints the same `kubectl` command and a `pulumi stack output` alternative. Ignore the Pulumi line,
there is no stack in this setup. The [stage runbook](../DEMO.md) walks through the beats.

## Network modes and sandbox profiles

Two skill-ci resources in the `skill-ci` namespace hold the platform settings. The chart creates one of each the first
time it is installed and never overwrites them afterwards (they carry `helm.sh/resource-policy: keep`), so you change
them with `kubectl edit`, `kubectl apply` or GitOps, not with Helm values. The controller picks up changes without a
restart.

| Resource | Holds |
| --- | --- |
| `SkillCIConfig` `default` (short name `scc`) | the default sandbox profile and the network policy: default mode, allowed modes, enforcement, bundles, always-allowed hosts |
| `SandboxProfile` (short name `sbp`), `default` and any you add | the trial sandbox: image (default: the release's sandbox image), CPU and memory (default 1 and 2Gi), warm pool size (default 2), `runtimeClassName`, `nodeSelector`, `tolerations` |

```bash
kubectl -n skill-ci get scc,sbp
kubectl -n skill-ci get sandboxtemplates,sandboxwarmpools   # what the controller made from the profiles
kubectl -n skill-ci edit scc default                         # e.g. spec.network.default: none
```

Each `SandboxProfile` becomes an Agent Sandbox `SandboxTemplate` and `SandboxWarmPool`: trials claim pre-started
sandboxes from the pool instead of starting a pod each time. A model list entry picks a profile with
`sandboxProfile: <name>`; without it the entry uses `spec.defaultSandboxProfile` of the `SkillCIConfig`. A bigger
profile for one agent:

```yaml
apiVersion: skillci.dev/v1alpha1
kind: SandboxProfile
metadata:
  name: large
  namespace: skill-ci
spec:
  resources: {cpu: "2", memory: 4Gi}
  warmPool: {replicas: 1}
```

A skill declares the network its trials may use in the `metadata` of its `SKILL.md`:

```yaml
metadata:
  skillci.dev/network: allowlist                       # none | allowlist | full
  skillci.dev/network-allow: "@python,dl.k8s.io"       # domains and bundles, for allowlist
```

`demo/push-skill.sh` copies both entries into OCI annotations of the artifact; with plain `oras push` add
`--annotation "skillci.dev/network=allowlist" --annotation "skillci.dev/network-allow=@python,dl.k8s.io"`.

| Mode | Trials can reach |
| --- | --- |
| `none` | DNS and the model API of the agent (for example `api.anthropic.com`) |
| `allowlist` | the same, plus the listed domains and bundles, TCP 443 |
| `full` (the default when the skill declares nothing) | the internet, never the cluster (no Harbor, no Kubernetes API) |

The `SkillCIConfig` decides what a skill may ask for: `spec.network.default` (for skills that declare nothing),
`spec.network.modes` (the allowed set), `spec.network.bundles` (`@python`, `@node`, `@github`, `@apt` by default),
`spec.network.alwaysAllow` and `spec.network.enforcement` (`auto`: Cilium when its CRD exists). With Cilium the
controller creates one `CiliumNetworkPolicy` per evaluation (`skillci-net-<run>`, the same for both arms) and a
baseline policy that leaves unclaimed warm sandboxes without egress; without Cilium only `full` runs, through Agent
Sandbox's own policy. The effective hosts of a run are in `kubectl -n skill-ci get skilleval <name> -o
jsonpath='{.status.network}'` and on its page of the reporting site; the runs page shows the policy in force. Details:
CONTRACT.md "SkillCIConfig", "SandboxProfile" and "Network modes".

GitOps tools that render charts with `helm template` (Argo CD) would apply the seed objects on every sync: set
`config.create=false` and `sandboxProfiles.default.create=false` there and keep the two objects in Git instead.

## Platform notes

| Platform | What to know |
| --- | --- |
| DOKS | Works as is. The default StorageClass `do-block-storage` is `ReadWriteOnce`. Enable the node pool autoscaler (for example min 2, max 5 of `s-4vcpu-8gb`). |
| EKS | Install the Amazon EBS CSI driver add-on and make sure a StorageClass is the default; without the driver the PVCs stay `Pending`. EBS volumes are zonal, so a pod can only run in the zone of its volume. |
| GKE | Works as is (`standard-rwo`). Autopilot is not verified. |
| AKS | Works as is (`managed-csi`). |
| kind, minikube | Not verified for this repository. Use at least three worker nodes or lower `controller.maxConcurrentJobs` to 1 (add `--set controller.maxConcurrentJobs=1` in step 8). There is no autoscaler, so a small laptop runs one evaluation at a time. Give Docker 12 GiB or more. Pods need DNS and egress to GHCR and the LLM APIs: on a VPN or behind a corporate resolver the node's `/etc/resolv.conf` may not work from inside kind, and CoreDNS then fails external lookups. Point CoreDNS at a resolver that works (`kubectl -n kube-system edit configmap coredns`, `forward . 1.1.1.1`). |

Volumes and `Recreate`: the Harbor chart is configured with `updateStrategy: Recreate`. It matters on every platform
with `ReadWriteOnce` block storage. With a `ReadWriteMany` StorageClass (NFS, CephFS) you can drop it.

Autoscaler: `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"` keeps Harbor and the toolbox on their nodes during scale-downs, and the controller sets it on the runner and sandbox
pods it creates. The values files and manifests already set it. On Karpenter, use
the annotation `karpenter.sh/do-not-disrupt: "true"` instead: add it under each `podAnnotations` in `helm/values/harbor.yaml`
and to the toolbox pod template.

## Versions and generated files

All versions of the vanilla path are in one file, [`versions.env`](versions.env): Agent Sandbox, the Harbor and
kube-prometheus-stack charts, the `oras` and `curl` images, and the skill-ci release (controller, runner, sandbox and
chart share one version). Nothing else repeats them. Install and uninstall scripts read it, the README commands take
it from `source deploy/versions.env`, and the image tags in the kustomizations are generated from it.

`infra/Pulumi.yaml` must use the same versions for the shared components. `deploy/check-versions.sh` compares them and
fails on a mismatch; run it after changing either file.

Some manifests are generated, never hand-edited:

| Generated file | Source |
| --- | --- |
| `manifests/monitoring/dashboard-configmap.yaml` | `infra/dashboards/skill-ci.json` |
| `manifests/harbor-setup/harbor-setup-script.yaml` | `infra/harbor-setup.sh` |
| `manifests/seed/skill-ci-models.yaml` | `demo/models.yaml` |
| `manifests/**/kustomization.yaml` | `versions.env` |

After changing a source, run `deploy/generate.sh`. `deploy/generate.sh --check` exits 1 when the files are out of date.

## Troubleshooting

| Symptom | Likely cause |
| --- | --- |
| Harbor pods stay `Pending` | No default StorageClass, or no node has room. `kubectl -n harbor describe pod <pod>` |
| Harbor upgrade hangs | A rolling update on `ReadWriteOnce` volumes. Keep `updateStrategy: Recreate`. |
| `harbor-setup` Job fails with `401` | The password in `harbor-credentials` differs from the one Harbor was installed with. |
| Pushes work but no `SkillEval` appears | The webhook policy is missing (rerun step 9), or `harbor-webhook` differs from the token in the policy (rerun step 9 after fixing the Secret). `kubectl -n skill-ci logs deployment/skill-ci-controller` |
| Runner Job pending | The cluster is full. Scale out, or lower `controller.maxConcurrentJobs`. |
| Runner Job fails at once | Check `kubectl -n skill-ci logs job/<name>`: a missing or invalid `anthropic` key in `llm-keys` fails the run before any work. |
| OpenCode runs fail with "provisioning key" | `openrouter` in `llm-keys` is an OpenRouter provisioning key. Replace it with an API key from openrouter.ai/settings/keys. |
| A run fails with `no eval dataset` | The skill has no `evals/evals.json`. Draft one with `make draft-evals SKILL=<name>`, review it, push again ([Skills need test cases](#skills-need-test-cases)). |
| Controller pod in `CreateContainerConfigError` | `otlp.headersSecret.name` names a Secret or key that does not exist in `skill-ci`. |
| Dashboard numbers doubled | The OTLP export pushes into the Prometheus that also scrapes the controller. Point `OTLP_ENDPOINT` elsewhere. |
| No OTLP data at the backend | `kubectl -n skill-ci logs deployment/skill-ci-controller \| grep otlp`: export errors are logged and never stop the controller. Check `OTLP_INSECURE` and `OTLP_URL_PATH`. |
| A run fails with `network mode "none" needs Cilium` | The cluster has no Cilium: only `full` can run. Remove the skill's `skillci.dev/network` entry or run on a Cilium cluster. |
| `kubectl get scc` shows `READY False` | The `SkillCIConfig` is invalid; its condition says why. The controller keeps the last valid settings (or the built-in defaults after a restart). |
| A run fails with `unknown network bundle` | The skill names a bundle the platform does not define. Add it to `spec.network.bundles` of `SkillCIConfig` `default`, or use domains. |
| A run fails with `sandbox profile "<name>" not found` | The model list entry names a `SandboxProfile` that does not exist: `kubectl -n skill-ci get sandboxprofiles`. |
| A trial fails with `InvalidMetadata` | Agent Sandbox refused the claim's labels. skill-ci uses the default allowed domain `sandbox.users.io`; a custom `agent-sandbox-config` with `allowed-label-domains` must keep it. |
| An agent cannot reach a host it needs | The skill's network mode blocks it. `hubble observe -n skill-ci --verdict DROPPED` shows the blocked lookups; add the domain or a bundle to `skillci.dev/network-allow`. |
| `ImagePullBackOff` on ghcr.io | The release exists but its GHCR packages are still private. Make them public in the package settings. |

## Teardown

```bash
deploy/uninstall.sh            # asks you to type the cluster context name first
deploy/uninstall.sh --yes --purge-crds   # no prompt; also removes the Prometheus Operator CRDs
```

This deletes the controller, every skill and evaluation, Harbor and its volumes, monitoring, the secrets and Agent
Sandbox. The cluster itself is not touched. Skills in Harbor are gone with it, so push them again after a reinstall.

By hand, in reverse order:

```bash
kubectl -n skill-ci delete skillevals --all
helm -n skill-ci uninstall skill-ci
kubectl delete crd skillevals.skillci.dev
helm -n monitoring uninstall kps
helm -n harbor uninstall harbor
kubectl delete namespace skill-ci monitoring harbor
kubectl delete -f "https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${AGENT_SANDBOX_VERSION}/sandbox-with-extensions.yaml"
```
