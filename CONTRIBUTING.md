# Contributing to skill-ci

Thanks for your interest in skill-ci. This guide covers how to propose a change, how to build and test each part of the repository, and what a pull request needs before it can be merged.

Everyone who takes part follows the [Code of Conduct](CODE_OF_CONDUCT.md). Report security issues privately as described in [SECURITY.md](SECURITY.md), never in a public issue.

## Proposing a change

- Bug fixes, small features and documentation fixes: open a pull request directly. Link the issue if there is one.
- Larger changes (a new CRD field, a change to the runner Job contract, the statistics or the tag policy, a new dependency, anything that changes how users install or upgrade): open an issue first and describe the problem and the change you have in mind. Wait for a maintainer to agree on the direction before you spend time on the code.
- [CONTRACT.md](CONTRACT.md) is the specification of the shared interfaces (names, CRDs, the runner Job contract, statistics, policy, metrics). A change to one of them updates CONTRACT.md in the same pull request.

Decisions follow [GOVERNANCE.md](GOVERNANCE.md).

## Repository layout

| Path | What it is | Language |
| --- | --- | --- |
| `controller/` | The skill-ci controller | Go |
| `runner/` | The runner image and the SkillEval Job script | Python, Dockerfile |
| `harbor-agent-sandbox/` | Harbor framework environment that runs each trial in an Agent Sandbox | Python |
| `sandbox/` | The trial sandbox image | Dockerfile, shell |
| `skillevaluator-patch/` | The patch the runner image applies to NVIDIA SkillEvaluator 0.6.0 | diff |
| `charts/skill-ci-controller/` | The Helm chart | Helm |
| `config/` | Plain manifests (`kubectl apply -k config/`) and the CRD source | YAML |
| `deploy/` | Install path with Helm and kubectl | shell, YAML |
| `infra/` | Pulumi YAML reference stack on DigitalOcean | Pulumi YAML |
| `tools/` | Generator of the offline report page | Python |
| `skills/`, `demo/` | Sample skills and the scripts behind the Makefile targets | Markdown, shell |

`make help` lists the Makefile targets.

## Building and testing

The CI workflows in `.github/workflows/` run the same commands as below. A pull request must pass all of them.

### Tools

- Go at the version in `controller/go.mod`
- Python 3.12 and [uv](https://docs.astral.sh/uv/)
- Docker with Buildx, for the images
- Helm and [kubeconform](https://github.com/yannh/kubeconform), for the chart
- shellcheck, Node.js (for `npx`) and [actionlint](https://github.com/rhysd/actionlint), for the linters
- [GoReleaser](https://goreleaser.com) v2, only for a release dry run

### Controller (Go)

```bash
cd controller
go vet ./...
go test -race ./...
go build ./...
golangci-lint run ./...        # CI uses golangci-lint v2.15.0
```

After you change the API types in `controller/api/`, regenerate the DeepCopy code and the CRD, then copy the CRD into the chart:

```bash
make -C controller generate manifests   # installs controller-gen v0.23.0 if missing
make chart-crds
```

CI fails when the generated files are not up to date. `make -C controller image` builds a development image for linux/amd64.

### Python (runner, harbor-agent-sandbox, tools)

The tests stub ORAS and SkillEvaluator, so you do not need the runner image to run them. From the repository root:

```bash
uv run --no-project --with-requirements .github/requirements-ci.txt --with ./harbor-agent-sandbox python -m pytest
uv run --no-project --with-requirements .github/requirements-ci.txt ruff check .
uv run --no-project --with-requirements .github/requirements-ci.txt ruff format --check .
```

`pyproject.toml` at the root sets the test paths (`runner/tests`, `harbor-agent-sandbox/tests`, `tools/tests`). The ruff settings are in `ruff.toml`.

### Images

```bash
docker build -f controller/Dockerfile controller   # controller
docker build -f runner/Dockerfile .                # runner; the build context is the repository root
make sandbox-image                                 # sandbox, tagged skill-ci-sandbox:local
```

The images are built for linux/amd64. The runner build applies the SkillEvaluator patch and runs `runner/check_patch.py`, which fails when the patch applies but no longer takes effect. The header of the patch explains how to regenerate it. The sandbox build fails when an agent CLI is not at its pinned release.

### Helm chart and install path

```bash
make chart-lint               # helm lint --strict, kubeconform, yamllint, packaging check
deploy/check-versions.sh      # deploy/versions.env and infra/Pulumi.yaml agree
deploy/generate.sh --check    # the generated manifests under deploy/manifests are up to date
```

### Linters

```bash
make lint             # shellcheck, yamllint, markdownlint, JSON files, dataset check of the sample skills
make lint-workflows   # actionlint
```

### Release dry run

```bash
make release-snapshot   # GoReleaser build of the controller binaries, archives and image; nothing is published
```

[docs/releasing.md](docs/releasing.md) describes the release process and the CI workflows in detail.

### Trying a change on a cluster

[deploy/README.md](deploy/README.md) installs skill-ci on any Kubernetes cluster with Helm and kubectl. A full evaluation needs LLM API keys; see [docs/agents-and-models.md](docs/agents-and-models.md). `make check-skills` checks the sample skills without any LLM call.

## Commit messages

Commits follow [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/): `type(scope): summary`, for example `fix(controller): allow the judge host in network none and allowlist`.

- Types in use: `feat`, `fix`, `docs`, `test`, `refactor`, `ci`, `chore`.
- Scopes in use: `controller`, `runner`, `sandbox`, `chart`, `deploy`, `infra`, `demo`, `dashboard`, `deps`. Leave the scope out when a change spans several parts.
- Start the summary in lower case and leave out the period at the end.
- Keep one logical change per commit.

## Developer Certificate of Origin

skill-ci uses the [Developer Certificate of Origin](https://developercertificate.org/) (DCO) instead of a contributor license agreement. By signing off a commit you certify that you wrote the change, or otherwise have the right to submit it under the project's license, as the DCO text describes.

Sign off every commit with `-s`:

```bash
git commit -s -m "fix(runner): say what is missing when a skill has no eval dataset"
```

This adds a line with the name and email from your Git configuration:

```text
Signed-off-by: Your Name <you@example.com>
```

The name and email must match the commit author. The DCO workflow (`.github/workflows/dco.yaml`) checks every commit of a pull request and fails when a sign-off is missing or does not match. It does not run on pushes to `main` or on pull requests the maintainer opens. To sign off commits you already made, run `git rebase --signoff main` and force-push the branch.

Commits made before the DCO check was added to the repository have no `Signed-off-by` line. Apart from the Dependabot dependency updates, which are signed off, all of them are the work of the project's sole author, Engin Diri, who released them under the Apache License 2.0 by adding the [LICENSE](LICENSE) file.

## Pull requests

- Keep a pull request focused on one change.
- Add or update tests for every behavior change: Go tests next to the code in `controller/`, pytest tests under `runner/tests`, `harbor-agent-sandbox/tests` or `tools/tests`.
- Update the documentation in the same pull request (see below).
- Fill in the pull request template.
- A maintainer reviews every pull request. Expect questions; they are part of the review.

## Documentation

| Where | What |
| --- | --- |
| [README.md](README.md) | Overview, requirements, quick start, versions |
| [docs/](docs/) | Topic guides: architecture, agents and models, the gate, network modes, observability, releasing, design notes, dependency licenses |
| [CONTRACT.md](CONTRACT.md) | The specification of names, CRDs, the runner Job contract, statistics, policy and metrics |
| [deploy/README.md](deploy/README.md) | Step-by-step install with Helm and kubectl |
| [charts/skill-ci-controller/README.md](charts/skill-ci-controller/README.md) | Chart values and CRD handling |

Markdown files are linted with markdownlint (`.markdownlint.yaml`). Prose lines are not wrapped.

## License

By contributing you agree that your contribution is licensed under the [Apache License 2.0](LICENSE).
