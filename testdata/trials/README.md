# Trial bundle fixtures

Golden trial bundles shared by the runner tests (`runner/trial_bundle.py`) and the controller tests (`controller/internal/session`, `controller/internal/reportsite`). The format is CONTRACT.md, "Trial bundles", and the schemas are `docs/schemas/skillci.trials.index.v1.json` and `docs/schemas/skillci.trial.v1.json`. Every file here validates against them.

## Layout

Each fixture is one bundle, as one runner Job would attach it:

| Directory | Source | What it covers |
| --- | --- | --- |
| `claude-code-doks/` | Real: the DOKS run `20261030_141344_87205_2043595da5fd` of `acme-release-notes` on `claude-code@2.1.292/claude-haiku-5-5` | One case, one attempt, both arms scored (0.95 and 0.36): Skill tool call, Write, the "Base directory for this skill" user step, evidence pointers |
| `claude-code-unscored/` | Real: the failed DOKS run `20261030_141159_86801_702d14d01af5` | Both arms `unscored` (the task image build failed), no trajectory, `failure`, `meta.exception` with traceback |
| `codex-synthetic/` | Synthetic, handwritten | `exec_command`, a failed `pip install`, `apply_patch`, reasoning; a network section with FORWARDED `api.openai.com` and `api.anthropic.com` and DROPPED `pypi.org` (`POLICY_DENIED`) |
| `opencode-synthetic/` | Synthetic, handwritten | `read` of SKILL.md, `write` then `edit` (an agent correction), `bash`; one output over 32 KiB, so `truncations` has an entry; network `unavailable` |

Synthetic trajectories say so in `trajectory.agent.extra.fixture`, their session id and run id start with `synthetic-`, and their IPs are from the documentation ranges (192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24).

`run-dir/` (real fixtures only) is the builder's input: the parts of SkillEvaluator's run directory that `runner/trial_bundle.py` reads (`result.json`, `dataset_snapshot.json`, `<agent>/<arm>/summary.json`, `<agent>/<arm>/trials/<id>/`). Paths of the machine the sample ran on are replaced with in-cluster ones (`/tmp/skill-eval-k3x9q2ab/...`, `registry.example.test`). `config.json` and `trial.log` are there because the builder must leave them out.

`bundle/` is the expected output.

## How the files map to the OCI layers

| File here | Layer title | Media type |
| --- | --- | --- |
| `bundle/index.json` | `index.json` | `application/vnd.skillci.trials.index.v1+json` |
| `bundle/trial-<id>.json` | `trial-<id>.json.gz` | `application/vnd.skillci.trial.v1+gzip` |

The files are stored uncompressed and indented for review. The layers hold the same documents serialized compactly (UTF-8, no spaces, no ASCII escaping) and gzip-compressed. `rawBytes` in the index is the size of that compact serialization and `bytes` the size after gzip at level 9; both depend on the serializer, so tests compare every other field and only check that `bytes` and `rawBytes` are positive.

The artifact type of the bundle manifest is `application/vnd.skillci.trials.v1+json`, with the annotations `skillci.dev/eval` (the `eval` field), `skillci.dev/agent`, `skillci.dev/agent-version`, `skillci.dev/model`, `skillci.dev/trials-schema: "1"` and `skillci.dev/redaction: v1`.

## Changing a fixture

The fixtures are frozen with the contract. A change goes through the owner of CONTRACT.md, and every file must still validate against the schemas. Never put a real credential here; `testdata/trials-secrets/` has the fake ones.
