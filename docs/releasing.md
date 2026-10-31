# Releases and CI

Every skill-ci release publishes the controller, the runner, the sandbox image and the Helm chart under one version. Nothing is built in the cluster.

## Releases

Pushing a tag `vX.Y.Z` runs `.github/workflows/release.yaml` and publishes everything from the tag with the workflow's `GITHUB_TOKEN` (no other secret). Tags with a suffix (`v0.7.0-rc.1`) are pre-releases and do not move `latest`.

| What | Where | Built by |
| ---- | ----- | -------- |
| Controller binaries (linux, darwin; amd64, arm64), checksums, SBOMs, release notes | GitHub release | GoReleaser (`.goreleaser.yaml`) |
| Controller image, linux/amd64 and linux/arm64 | `ghcr.io/dirien/skill-ci-controller:X.Y.Z` | GoReleaser, `controller/Dockerfile.goreleaser` |
| Runner image, linux/amd64 | `ghcr.io/dirien/skill-ci-runner:X.Y.Z` | `docker/build-push-action`, `runner/Dockerfile` |
| Sandbox image, linux/amd64 | `ghcr.io/dirien/skill-ci-sandbox:X.Y.Z` | `docker/build-push-action`, `sandbox/Dockerfile` |
| Helm chart, version and appVersion `X.Y.Z` | `oci://ghcr.io/dirien/charts/skill-ci-controller` | `helm package` + `helm push` |

The checksums file and the three images are signed with Cosign (keyless, the workflow identity). The runner and sandbox images carry an SBOM and a provenance attestation.

The chart in the repository and the published chart are the same: the default images are `ghcr.io/dirien/skill-ci-controller`, `skill-ci-runner` and `skill-ci-sandbox`, tagged with the chart's `appVersion` (which `helm package --app-version X.Y.Z` sets from the release tag), and `imagePullSecrets` is empty.

```bash
helm install skill-ci oci://ghcr.io/dirien/charts/skill-ci-controller --version X.Y.Z \
  --namespace skill-ci --create-namespace
```

The GHCR packages of a private repository are private. Make the four packages (three images and the chart) public in the package settings, which is how they are meant to be used, or give the cluster pull credentials: a `docker-registry` Secret in the namespace and `--set 'imagePullSecrets[0].name=ghcr'`, which the chart uses for the controller and passes to the controller for the runner Jobs and the sandboxes.

Helm never upgrades `crds/`, so apply the CRDs of the new version before an upgrade:

```bash
helm show crds oci://ghcr.io/dirien/charts/skill-ci-controller --version X.Y.Z | kubectl apply --server-side -f -
```

On the Pulumi reference stack, `make deploy` installs a release with `pulumi up`: the published OCI chart of the stack's `skillCiVersion` (default in `infra/Pulumi.yaml`, the same as `SKILL_CI_VERSION` in `deploy/versions.env`) and the controller, runner and sandbox images of that release. `make deploy TAG=0.8.1` sets the version first.

Two targets help before a release: `make release-snapshot` dry-runs the GoReleaser build locally (needs `goreleaser` and Docker), and `make sandbox-image` builds the sandbox image locally as `skill-ci-sandbox:local`.

## The Helm chart

`charts/skill-ci-controller/` packages the controller (CRDs, RBAC, runner ServiceAccount, Deployment, Service, optional ServiceMonitor) as a Helm 4 chart, published to GHCR as an OCI artifact on every release. `kubectl apply -k config/` still works as a plain-manifest alternative.

- `make chart-crds` copies `config/crd/` into the chart. `crds/` is generated, never edited by hand.
- `make chart-lint` runs `helm lint --strict`, kubeconform, yamllint and a packaging check.

The [chart README](../charts/skill-ci-controller/README.md) covers the values, adopting an existing install with `--take-ownership`, and pushing the chart to an OCI registry of your own.

## Versions in the repository

[deploy/versions.env](../deploy/versions.env) is the single source of the versions for the Helm and kubectl install: Agent Sandbox, the Harbor and kube-prometheus-stack charts, the `oras` and `curl` images, and the skill-ci release. `infra/Pulumi.yaml` must use the same values for the shared components, and `deploy/check-versions.sh` fails on a mismatch. Some manifests under `deploy/manifests` are generated from other files; run `deploy/generate.sh` after changing a source, and `deploy/generate.sh --check` exits 1 when they are out of date.

## CI

| Workflow | Trigger | Checks |
| -------- | ------- | ------ |
| `ci.yaml` | push and PR on main | `go vet`, `go test -race`, `go build`; pytest; Docker build of the controller, runner and sandbox images (no push; the sandbox build installs and version-checks the three agent CLIs); `goreleaser check` and a snapshot release |
| `lint.yaml` | push and PR on main | golangci-lint; ruff check and format; `make lint` (shellcheck, yamllint, markdownlint, JSON, sample skills); actionlint |
| `chart.yaml` | push and PR on main | generated code and CRDs up to date; `make chart-lint`; the chart packages and renders with the GHCR images of its appVersion and no pull secret |
| `release.yaml` | tag `v*` | tests, then GoReleaser, runner image, sandbox image and chart as above |

Dependabot (`.github/dependabot.yaml`) keeps the actions, Go modules, Python pins and Dockerfile base images current.

Run the same checks locally with `make lint`, `make chart-lint` and `make lint-workflows` (actionlint).
