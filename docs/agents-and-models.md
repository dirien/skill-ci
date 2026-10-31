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

The judge that scores every run is always Anthropic, whatever agent runs. That is why `anthropic` is the only required key in the Secret `llm-keys`. SkillEvaluator hands the agent only the credentials of its own route, never the judge key.

| Agent | `model` | Provider route |
| --- | --- | --- |
| `claude-code` | a Claude model, `claude-opus-5-5` | `ANTHROPIC_API_KEY` |
| `codex` | an OpenAI model, `openai/<id>` | `OPENAI_API_KEY` and `OPENAI_BASE_URL` (default `https://api.openai.com/v1`) |
| `opencode` | `<provider>/<model id>` | `anthropic/...` runs natively with the Anthropic key; `ollama-cloud/...`, `openrouter/...`, `nvidia/...` and `openai/...` run through that provider's OpenAI-compatible endpoint with its key |

The keys of `llm-keys`:

| Key | Needed for |
| --- | --- |
| `anthropic` | Required: the judge, Claude Code, OpenCode `anthropic/...` models |
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

## Upgrading from 0.5

Runs created before agent versions existed have no `agent-version` label and count as the entries without `agentVersion`. Pinned entries are new entries, so applying the pinned list evaluates the `beta` and `stable` digests again. To avoid mixing, keep an unpinned entry in the list, or delete the old SkillEvals (`make reset`).
