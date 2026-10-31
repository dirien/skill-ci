# Agents and models

The harness is part of the measurement, so skill-ci treats the agent and its release like the model. An entry of the model list is agent + `agentVersion` + model, and that triple is the identity of the entry.

## The model list

The model list is the ConfigMap `skill-ci-models`, key `models.yaml`. The controller watches it, so you can edit it while it runs. [demo/models.yaml](../demo/models.yaml) is the list the install seeds, with one entry for each supported agent:

```yaml
models:
  - agent: claude-code
    agentVersion: "2.1.292"
    model: claude-opus-5-5
    required: true
  - agent: codex
    agentVersion: "0.161.0"
    model: openai/gpt-6.1-sol
    required: false
  - agent: opencode
    agentVersion: "1.19.1"
    model: openrouter/qwen/qwen3.8-flash
    required: false
```

| Field | Meaning |
| --- | --- |
| `agent` | The SkillEvaluator and Harbor framework agent: `claude-code`, `codex` or `opencode` |
| `agentVersion` | Optional. The release of the agent CLI. Without it the runner uses its default pin and the controller records the release that ran in `status.agentVersion` |
| `model` | The model the agent runs, in the format of the agent (see below) |
| `required` | `true` gates promotion. `false` runs and is reported everywhere, but never holds a version back |
| `sandboxProfile` | Optional. The `SandboxProfile` the trials run in. Not part of the identity |

Pin every entry to keep runs comparable. An unpinned entry and a pinned one are different entries even when they end up on the same release. The full schema is in [CONTRACT.md, "Model list"](../CONTRACT.md).

## Agent versions

| Agent | Pinned release | Model | Needs in `llm-keys` |
| --- | --- | --- | --- |
| `claude-code` | 2.1.292 (SkillEvaluator's pin) | `claude-opus-5-5` | `anthropic` |
| `codex` | 0.161.0 (SkillEvaluator's pin) | `openai/<id>` | `openai`, optionally `openai-base-url` |
| `opencode` | 1.19.1 (skill-ci's pin: upstream pins none, 1.19.1 was published on 2026-10-15) | `ollama-cloud/gpt-oss:120b`, `anthropic/<model>`, ... | `ollama` (or `openrouter`, `nvidia`, `anthropic`) |

The sandbox image ships these three releases. An `agentVersion` that differs from the pin is installed in each trial sandbox with npm, which is slower, so `TIMEOUT_MULTIPLIER` on the runner matters for it, and with network mode `none` or `allowlist` the run also gets the `SkillCIConfig` `network.agentInstallHosts`. The controller learns the preinstalled releases from `--preinstalled-agents` (Helm `controller.preinstalledAgents`).

A new agent version is a new entry, so bumping a version evaluates every `beta` and `stable` digest on it again. Results stay per entry: the regression gate compares a candidate with the stable version on the same `agent@version/model`, never across harnesses.

## Required and optional entries

`required` per entry decides what gates promotion. The seeded list gates on Claude Code and runs Codex and OpenCode as optional, so their results show up in the reporting site and the metrics without holding a skill back.

Changing `required` on an existing entry updates `spec.required` of its existing SkillEvals, and the policy runs again at once. `make demo-model-swap MODE=require AGENT=codex` makes Codex gate, and `MODE=optional AGENT=codex` undoes it. Runs of removed entries stay and keep gating while they are required, so delete them (or run `make reset`) when you retire a required entry.

## Models per agent and LLM keys

Each agent needs the key of its own route, and the judge (see [The judge](#the-judge)) needs its key, Anthropic's `anthropic` by default. SkillEvaluator hands the agent the credentials of its own route: under a judge of the agent's own provider (Claude Code under `anthropic`, Codex under `openai`) that is the judge's key and endpoint, under any other judge the agent never sees the judge's key.

| Agent | `model` | Provider route |
| --- | --- | --- |
| `claude-code` | a Claude model, `claude-opus-5-5` | `ANTHROPIC_API_KEY` |
| `codex` | an OpenAI model, `openai/<id>` | `OPENAI_API_KEY` and `OPENAI_BASE_URL` (default `https://api.openai.com/v1`) |
| `opencode` | `<provider>/<model id>` | `anthropic/...` runs natively with the Anthropic key; `ollama-cloud/...`, `openrouter/...`, `nvidia/...` and `openai/...` run through that provider's OpenAI-compatible endpoint with its key |

The keys of `llm-keys`:

| Key | Needed for |
| --- | --- |
| `anthropic` | The default judge, Claude Code, OpenCode `anthropic/...` models. Required for those; a Job of such an agent does not start without it |
| `openai` | Codex, Tier 2 embeddings |
| `openai-base-url` | Codex against a compatible endpoint; defaults to `https://api.openai.com/v1` |
| `openrouter` | OpenCode `openrouter/...` models. An API key, not a provisioning key: the runner fails the run when it gets one |
| `ollama` | OpenCode `ollama-cloud/...` models |
| `nvidia` | OpenCode `nvidia/...` models; Tier 2 embeddings when `openai` is missing |

Create the Secret by hand and never commit key values:

```bash
kubectl -n skill-ci create secret generic llm-keys --from-literal=anthropic="$ANTHROPIC_API_KEY"
```

An optional entry whose key is missing fails that run before any work and, being optional, never gates, so with only an Anthropic key the Claude Code entry still decides. OpenCode models other than `anthropic/...` use the OpenAI-compatible gateway route that patch hunks 6 and 8 to 12 open. The routes and the checks are in [CONTRACT.md](../CONTRACT.md), "Agent versions" and "LLM keys".

## The judge

An LLM judge scores every trial, whatever agent is under test. It is a platform setting: `spec.judge` of the `SkillCIConfig` `default`, seeded by the chart value `config.judge`.

```yaml
spec:
  judge:
    provider: anthropic     # anthropic | openai | openai-compatible | nv_build
    model: ""               # empty: SkillEvaluator's default model of the provider
    baseURL: ""             # API root; required for openai-compatible
    secretKey: anthropic    # the key of llm-keys that holds the judge's API key
```

The default is Anthropic with the key `anthropic`, so an install that sets nothing scores as before. The controller passes the judge in force to every new runner Job: `SKILL_EVAL_LLM_PROVIDER`, `SKILL_EVAL_LLM_MODEL` and `SKILL_EVAL_LLM_BASE_URL` (the last two only when set, since SkillEvaluator rejects an empty model) and `SKILL_EVAL_LLM_API_KEY` from `llm-keys/<secretKey>`. That key is required: a Job whose judge key is missing does not start. An edit applies to runs that start after it, without a restart. The `Ready` condition of the `SkillCIConfig` and `status.effective.judge` show the judge in force; an invalid judge (an unknown provider, `openai-compatible` without `baseURL`, a `baseURL` with credentials or a query) leaves `Ready` `False` and keeps the last valid spec.

| `provider` | Endpoint | Host allowed with network `none` and `allowlist` |
| --- | --- | --- |
| `anthropic` | The Anthropic Messages API, or `baseURL` (an API root such as a proxy) | `api.anthropic.com`, or the host of `baseURL` |
| `openai` | `https://api.openai.com/v1`, or `llm-keys` `openai-base-url`, or `baseURL` | The host of `baseURL`, else of `openai-base-url`, else `api.openai.com` |
| `openai-compatible` | Any OpenAI-compatible endpoint at `baseURL` (required) | The host of `baseURL` |
| `nv_build` | NVIDIA Build (`baseURL` is not allowed) | `integrate.api.nvidia.com` |

SkillEvaluator stages the judge's credentials into the Harbor verifier, which runs inside the trial pod, so the judge's host is allowed in the network modes `none` and `allowlist` next to the agent's model host.

The runner hands `SKILL_EVAL_LLM_API_KEY` to SkillEvaluator under the variable the provider reads (`ANTHROPIC_API_KEY` or `NVIDIA_API_KEY` when that is unset; `openai` and `openai-compatible` read it directly), and it accepts only these agent and judge combinations:

| Agent | Judge providers |
| --- | --- |
| `claude-code` | `anthropic`, `openai`, `openai-compatible`, `nv_build` |
| `codex` | `anthropic`, `openai`, `nv_build` |
| `opencode` | `anthropic` |

The controller checks the combination before it creates a Job: a run whose agent the judge cannot score ends `Failed` with `judge provider "<p>" cannot score agent "<a>"` and no Job. SkillEvaluator also knows `bedrock`; skill-ci does not offer it, because no agent of the runner can be scored with it and its host depends on the runner's AWS region.

A judge on another provider, with its key in `llm-keys` under the name `gateway`:

```bash
kubectl -n skill-ci patch secret llm-keys --type merge -p '{"stringData":{"gateway":"<key>"}}'
kubectl -n skill-ci patch scc default --type merge \
  -p '{"spec":{"judge":{"provider":"openai-compatible","baseURL":"https://llm.example.com/v1","model":"<model>","secretKey":"gateway"}}}'
kubectl -n skill-ci get scc default -o jsonpath='{.status.effective.judge}'
```

Scores from different judges are not comparable. Changing the judge does not evaluate the digests again, and the regression gate compares a candidate with a stable version that a different judge may have scored, so change it between releases of your skills, not in the middle of one.

## Upgrading from 0.5

Runs created before agent versions existed have no `agent-version` label and count as the entries without `agentVersion`. Pinned entries are new entries, so applying the pinned list evaluates the `beta` and `stable` digests again. To avoid mixing, keep an unpinned entry in the list, or delete the old SkillEvals (`make reset`).
