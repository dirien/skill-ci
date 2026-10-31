# Network modes and sandboxes

Every trial runs in its own Agent Sandbox pod. Two skill-ci resources in the controller namespace decide what that pod looks like and what it may reach: `SandboxProfile` for the pod and `SkillCIConfig` for the platform policy. The skill itself declares the network it needs. The specification is in [CONTRACT.md](../CONTRACT.md), sections "SkillCIConfig", "SandboxProfile" and "Network modes".

## Network modes

A skill declares what its trials may reach in the `metadata` of its `SKILL.md`:

```yaml
metadata:
  skillci.dev/network: allowlist                       # none | allowlist | full
  skillci.dev/network-allow: "@python,dl.k8s.io"       # domains and bundles, for allowlist
```

`demo/push-skill.sh` copies both entries into OCI annotations of the artifact. With plain `oras push`, add `--annotation "skillci.dev/network=allowlist" --annotation "skillci.dev/network-allow=@python,dl.k8s.io"`.

| Mode | Trials can reach |
| --- | --- |
| `none` | DNS, the model API of the agent and the judge's API (`api.anthropic.com` by default, see `spec.judge`; the Tier 3 verifier calls it from the trial pod) |
| `allowlist` | the same, plus the listed domains and bundles, TCP 443 |
| `full` (the default when the skill declares nothing) | the internet, never the cluster (no Harbor, no Kubernetes API) |

The same policy applies to both arms of a run, with and without the skill. With Cilium the controller creates one `CiliumNetworkPolicy` per evaluation (`skillci-net-<run>`) and a baseline policy that leaves unclaimed warm sandboxes without egress. Without Cilium only `full` runs, through Agent Sandbox's own policy, and a skill that asks for `none` or `allowlist` fails before any Job with `network mode "none" needs Cilium`. Check for Cilium with `kubectl get crd ciliumnetworkpolicies.cilium.io`.

The effective hosts of a run are in `kubectl -n skill-ci get skilleval <name> -o jsonpath='{.status.network}'` and on its page of the reporting site. With `spec.observability.hubbleRelay` set, the trial session of the reporting site shows the DNS names and connections of every trial, forwarded and dropped. Without it, `hubble observe -n skill-ci --verdict DROPPED` shows the blocked lookups. Add the domain or a bundle to `skillci.dev/network-allow`.

Only trial pods are restricted. Neither policy of skill-ci selects the runner pod of a Job (the baseline selects `skillci.dev/component: trial-sandbox`, the eval policy `sandbox.users.io/skillci-eval` and `sandbox.users.io/skillci-role: trial`, labels the runner pod does not carry), and the chart creates no NetworkPolicy, so the runner reaches Harbor, the LLM APIs and Hubble Relay on any cluster that has no default-deny policy of its own. A cluster that has one must allow the runner pods (label `batch.kubernetes.io/job-name`, ServiceAccount `skill-eval-runner`) those destinations.

## SkillCIConfig

`SkillCIConfig` `default` (short name `scc`) holds the platform settings of one install. The controller reads only the object named `default` in its own namespace.

| Field | Default | Meaning |
| --- | --- | --- |
| `spec.defaultSandboxProfile` | `default` | The `SandboxProfile` of a model list entry without `sandboxProfile` |
| `spec.network.default` | `full` | Mode of a skill that declares none |
| `spec.network.modes` | `[none, allowlist, full]` | Modes a skill may declare |
| `spec.network.enforcement` | `auto` | `auto`: Cilium when its CRD exists; `cilium`: require it; `off`: no policies |
| `spec.network.alwaysAllow` | empty | Hosts added to `none` and `allowlist`, for example a proxy |
| `spec.network.bundles` | `@apt`, `@github`, `@node`, `@python` | Named host lists a skill can use in `skillci.dev/network-allow` |
| `spec.network.agentInstallHosts` | `[registry.npmjs.org]` | Hosts added for an agent version that is not preinstalled |
| `spec.observability.hubbleRelay` | empty | `host:port` of the Hubble Relay the runner reads network flows from; empty collects none. See [Observability](observability.md#network-flows) |
| `spec.judge` | `{provider: anthropic, secretKey: anthropic}` | The LLM judge of every run and its key in `llm-keys`. See [Agents and models](agents-and-models.md#the-judge) |

The controller reads the object whenever it resolves a `Pending` run, so an edit applies to the next run without a restart. A run that is already running keeps its settings. An invalid edit leaves `Ready` `False` with the reason, and the controller keeps the last valid spec (or the built-in defaults after a restart).

```bash
kubectl -n skill-ci get scc,sbp
kubectl -n skill-ci edit scc default                         # e.g. spec.network.default: none
```

## SandboxProfile and warm pools

A `SandboxProfile` (short name `sbp`) describes the trial sandbox: image (default: the release's sandbox image), CPU and memory (default 1 and 2Gi), warm pool size (default 2), and optionally `runtimeClassName`, `nodeSelector` and `tolerations`.

The controller turns each profile into an Agent Sandbox `SandboxTemplate` and `SandboxWarmPool`, so trials claim pre-started sandboxes from the pool instead of starting a pod each time. A model list entry picks a profile with `sandboxProfile: <name>`; without it the entry uses `spec.defaultSandboxProfile` of the `SkillCIConfig`. A bigger profile for one agent:

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

Without the Agent Sandbox extensions CRDs a profile reports `Ready` `False` with reason `AgentSandboxExtensionsMissing`, and runs start a bare `Sandbox` per trial. A model list entry that names a profile that does not exist fails with `sandbox profile "<name>" not found` and no Job.

```bash
kubectl -n skill-ci get sandboxtemplates,sandboxwarmpools   # what the controller made from the profiles
```

## Who owns the objects

The chart creates `SkillCIConfig` `default` and the `default` `SandboxProfile` once, the first time it is installed, and never overwrites them afterwards (they carry `helm.sh/resource-policy: keep`). Manage them with `kubectl edit`, `kubectl apply` or GitOps, not with Helm values. GitOps tools that render charts with `helm template` (Argo CD) would apply the seed objects on every sync: set `config.create=false` and `sandboxProfiles.default.create=false` there and keep the two objects in Git instead. The [chart README](../charts/skill-ci-controller/README.md) has the details.

## Sandbox sizing and concurrency

Each trial runs in one sandbox. With a warm pool, the `SandboxProfile` decides the size. For a bare `Sandbox` per trial, the runner image defaults apply: `SANDBOX_CPUS=1` and `SANDBOX_MEMORY_MB=2048` per sandbox, and `TRIAL_CONCURRENCY=2` trials at once per Job. `SANDBOX_NAMESPACE` and `SANDBOX_IMAGE` (the sandbox image of the runner's release) are set there too; the controller overrides `SANDBOX_IMAGE` from `--sandbox-image` (Helm `controller.sandboxImage`). Override the others as env vars on the runner container; the manual Job in `runner/job.yaml` shows where.

The controller caps the Jobs: Helm value `controller.maxConcurrentJobs` (`--max-concurrent-jobs`, deployed with 3) and `controller.trialBudget` (`--trial-budget`, 200 trials per controller lifetime, 0 is unlimited). Three Jobs at two trials each is up to 6 sandboxes of 1 vCPU and 2 GiB at a time. `--dry-run` creates SkillEvals in phase `Planned` and no Jobs. Live evaluations cost LLM tokens, so keep these limits in mind; [CONTRACT.md, "Cost guard"](../CONTRACT.md) lists the flags.

Runner and trial pods carry `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"`, so a node scale-down never kills a running evaluation. Sandboxes created by a runner pod are owned by that pod, so cancelling a run deletes them.
