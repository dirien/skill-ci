# Redaction fixtures (fake secrets)

Everything in this directory is fake. The values only have the shapes of real credentials (`sk-ant-test-0000...`, `nvapi-test-0000...`, an Ollama-style `<32 hex>.<24+ characters>` key, a `ghp_` token). Do not copy them anywhere else, and keep this directory out of anything that is published.

`canary/` is a synthetic claude-code trial whose agent printed its environment and the keys in several encodings:

| File | Use |
| --- | --- |
| `secrets.env` | The fake values as the runner would see them (`SECRET_ENV`, `HARBOR_PASSWORD`, the registry `auth` value), plus a GitHub token and an environment dump line that only the pattern and env-dump rules catch |
| `auth-config.json` | A fake `/auth/config.json` (registry credentials) |
| `run-dir/` | The bundle builder's input, in SkillEvaluator's layout. Holds every value raw, base64, URL-encoded and JSON-escaped, in observations, the final message, `reward.json` and the agent log |
| `bundle-unredacted/` | What a runner with no redaction would upload. For the controller's render-time redaction tests: no value of `secrets.env`, in any form, may reach an API response |

Tests built on it:

- Runner (`runner/tests/test_trial_bundle.py`): building `run-dir/` with the values of `secrets.env` leaves none of their forms in any layer, the counts in `redaction` are non-zero, and a value the rules miss withholds the trial (fail closed).
- Controller (`internal/session`, `internal/reportsite`): a session built from `bundle-unredacted/` with the `llm-keys` values contains none of them.
