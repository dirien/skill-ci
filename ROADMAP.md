# Roadmap

This roadmap lists the work the maintainers plan next. It has no dates; items move when someone works on them. To discuss an item or propose a new one, open an issue.

## Done recently

- Trial session viewer (0.11.0): the reporting UI shows what the agent did in every trial, with and without the skill, with secrets redacted before anything is stored.
- Configurable judge (0.11.0): `SkillCIConfig` `spec.judge` sets the provider, model and endpoint; Anthropic stays the default.
- Retries per trial for rate limits (0.11.1): Harbor retries a rate-limited trial with a backoff before the whole run is retried.

## Planned

### Distribution to developer machines

Today developers pull a channel with ORAS, for example `oras pull <registry>/skills/<skill>:stable -o ~/.claude/skills/<skill>`. Two directions are open. A small skill-ci CLI could resolve a channel to a digest, install only a digest that has a passing eval report attached, and keep a lock file. The other is to follow the packaging the Agent Skills project settles on: an open proposal under the Agentic AI Foundation describes skills as OCI artifacts with media types, lock files and signatures (agentskills/agentskills discussion 292). skill-ci would push that format and keep reading its current one, and installers built for the format could then read any skill-ci registry.

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
