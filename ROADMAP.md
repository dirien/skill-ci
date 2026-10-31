# Roadmap

This roadmap lists the work the maintainers plan next. It has no dates; items move when someone works on them. To discuss an item or propose a new one, open an issue.

## In progress

### Trial session viewer

The reporting site shows each SkillEval and its SkillEvaluator report. The next step is a viewer for the session of every trial: what the agent did, step by step, with and without the skill. Secrets are redacted before anything is shown.

## Planned

### Configurable judge

Today Anthropic is the judge of every run: the `llm-keys` Secret needs the key `anthropic`, and the network modes `none` and `allowlist` always allow `api.anthropic.com`. SkillEvaluator already supports other providers (OpenAI, Bedrock, NVIDIA Build and OpenAI-compatible endpoints). The plan is to set the judge provider, model and host in `SkillCIConfig`, derive the allowed judge host from it, and document a setup that uses only a self-hosted, OpenAI-compatible endpoint. The same applies to the embeddings that Tier 2 uses.

### Retries per trial for rate limits

A run that failed only because a provider rate-limited it is retried as a whole. Retrying only the trials that hit the limit would save time and tokens.

### End-to-end tests on kind

CI runs unit tests for the controller and the Python parts. The plan is an end-to-end job on a kind cluster that installs the chart and runs a skill through Tier 1 and Tier 2, or through Tier 3 with a stub agent, so that it needs no LLM key. envtest tests for the reconcilers belong to the same work.

### Supported Kubernetes versions

The chart declares `kubeVersion: >=1.27.0-0`, the full install asks for 1.30 or newer, and the version tested by hand is 1.36 on DOKS. The plan is a written support policy (for example, the three newest minor releases), a kind test matrix that covers it, and a statement of the supported CPU architectures. The runner and sandbox images are linux/amd64 only today.

### Upstream the SkillEvaluator patch

The runner image applies a patch to SkillEvaluator 0.6.0 (`skillevaluator-patch/`). The changes will be proposed to [NVIDIA SkillEvaluator](https://github.com/NVIDIA/SkillEvaluator) so that skill-ci can use an unpatched release.

### Documentation site

A documentation site on skillci.dev, built from `docs/`.

### Neutral GitHub organization

Move the repository from `github.com/dirien/skill-ci` to a GitHub organization that does not belong to one person. That changes the Go module path, the image names on GHCR and the chart location, so it will come with upgrade notes.

### CNCF Sandbox application

Apply to the CNCF Sandbox once the project meets what the TOC looks for: a neutral home, more than one maintainer, outside adopters, about six months of public history, and license exceptions requested for the dependencies listed in [docs/dependency-licenses.md](docs/dependency-licenses.md).
