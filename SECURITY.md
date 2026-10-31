# Security policy

## Supported versions

skill-ci is before 1.0. Security fixes go into the latest minor release only, as a new patch release. The controller, runner, sandbox image and Helm chart share one version.

| Version | Supported |
| --- | --- |
| 0.10.x | Yes |
| Older than 0.10 | No, upgrade to the latest release |

## Reporting a vulnerability

Do not open a public issue, pull request or discussion for a security problem.

Report it privately with GitHub private vulnerability reporting: open the [Security tab](https://github.com/dirien/skill-ci/security) of the repository and choose "Report a vulnerability", or go to [the new advisory form](https://github.com/dirien/skill-ci/security/advisories/new) directly.

Please include:

- the affected part and version (controller, runner, sandbox image, chart),
- how to reproduce it, with the configuration that matters (network mode, `SkillCIConfig`, `SandboxProfile`, chart values),
- what an attacker can do with it.

Remove API keys, registry credentials and other secrets from logs and reports before you attach them.

## What to expect

skill-ci has a single maintainer, so the targets below are modest and best effort:

- an acknowledgment of your report within 7 days,
- a first assessment (accepted or not, and a severity) within 14 days,
- regular updates in the advisory until the issue is fixed.

The maintainer fixes accepted vulnerabilities in a patch release of the supported version, then publishes a GitHub security advisory that credits you, unless you prefer not to be named. The goal is to publish within 90 days of the report. If a fix takes longer, the maintainer will agree a date with you.

## Scope

In scope:

- The controller: the Harbor webhook endpoint and its token check, the reconcilers, the RBAC it needs, the reporting site and its API.
- The runner image and the runner Job: how it pulls skills, handles LLM keys and registry credentials, and uploads reports.
- The sandbox image and the trial sandboxes: isolation of a skill under test from the cluster, from the Kubernetes API and from other trials.
- The Helm chart and the manifests in `config/` and `deploy/`: default permissions, Secrets, exposed Services.
- The network policies for the network modes `none`, `allowlist` and `full`: a trial that reaches a host its mode does not allow, or reaches the cluster at all.
- Redaction of secrets: API keys, tokens or credentials that appear in eval reports, trial session data, the reporting site, logs or metrics.

Out of scope:

- Vulnerabilities in upstream projects such as goharbor, Agent Sandbox, Cilium, SkillEvaluator, the Harbor framework or the agent CLIs. Report them to those projects. Do tell us if the way skill-ci uses a component makes a known issue exploitable.
- What a skill under test does inside the limits of its declared network mode. skill-ci treats every skill as untrusted input and is responsible for keeping it inside its sandbox and network mode, not for what it does there.
- The Pulumi reference stack in `infra/` and the demo scripts in `demo/`, which are examples and not meant for production as they are.
