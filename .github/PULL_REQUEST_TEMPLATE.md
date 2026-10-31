<!-- markdownlint-disable-file MD041 -->

## What this changes

<!-- What the pull request does and why. Link the issue it fixes, for example "Fixes #12". -->

## How it was tested

<!-- The tests you added or ran, and for changes that affect a cluster, where you tried them. -->

## Checklist

- [ ] Every commit is signed off (`git commit -s`), as the [DCO](https://developercertificate.org/) requires. See [CONTRIBUTING.md](https://github.com/dirien/skill-ci/blob/main/CONTRIBUTING.md#developer-certificate-of-origin).
- [ ] Commit messages follow Conventional Commits (`fix(controller): ...`).
- [ ] Tests cover the change, and `go test -race ./...` (in `controller/`) and `pytest` pass.
- [ ] `make lint` passes, and `make chart-lint` if the chart or CRDs changed.
- [ ] Generated files are up to date (`make -C controller generate manifests`, `make chart-crds`, `deploy/generate.sh`).
- [ ] Documentation is updated: `docs/`, `CONTRACT.md` for interface changes, the chart README for new values, `CHANGELOG.md` under "Unreleased".
