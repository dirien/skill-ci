# Dependency licenses

skill-ci is licensed under the [Apache License 2.0](../LICENSE). This page lists the third-party components that skill-ci ships or installs, their licenses, and which of them fall outside the [CNCF allowlist](https://github.com/cncf/foundation/blob/main/policies-guidance/allowed-third-party-license-policy.md). A CNCF project may use Apache-2.0 components and allowlisted components without review. Every other license needs an exception from the CNCF Governing Board, requested with the [license exception issue template](https://github.com/cncf/foundation/issues/new?template=license-exception-request.yaml) when the project applies.

The list describes release 0.10.1 and was checked on 2026-10-31. Update it when a dependency, a pin or an image changes. The reporting UI embedded in the controller is listed under "Reporting UI (npm)".

## How the licenses were checked

| Part | Method |
| --- | --- |
| Controller (Go) | `go-licenses report ./cmd/manager` ([google/go-licenses](https://github.com/google/go-licenses) v2.0.1) in `controller/`, grouped by module with `go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./cmd/manager` |
| Python packages in the runner and sandbox images | The `License-Expression`, `License` and `License ::` classifier fields of every `*.dist-info/METADATA` file in the published images `ghcr.io/dirien/skill-ci-runner:0.10.1` and `ghcr.io/dirien/skill-ci-sandbox:0.10.1` (extracted with `docker export`), and the bundled-library sections of the numpy and scipy `LICENSE.txt` files |
| npm packages in the sandbox image | The `license` field of each global package's `package.json` in the sandbox image |
| Debian packages in the images | The `License:` fields of `/usr/share/doc/<package>/copyright` in each image |
| Binaries and tools fetched at build time (ORAS, Gitleaks, Semgrep, SkillSpector, SkillEvaluator) | Package metadata in the image where there is any, and the license that GitHub reports for the upstream repository (`gh api repos/<owner>/<repo>/license`) |
| Claude Code | The `LICENSE.md` of [anthropics/claude-code](https://github.com/anthropics/claude-code/blob/main/LICENSE.md) |

The runner and sandbox images also carry an SBOM attestation from the release build (see [releasing.md](releasing.md)), which lists every package with its version.

## Summary of components outside the allowlist

Each of these needs a license exception request when skill-ci applies to the CNCF Sandbox, or a change that removes the component.

| Component | Where | License | Notes |
| --- | --- | --- | --- |
| Semgrep CE 1.183.0 | runner image (`/opt/semgrep`), installed from PyPI at build time | LGPL-2.1-or-later | A Tier 1 scanner of SkillEvaluator. The CNCF Governing Board approved LGPL-2.1-or-later for another project in October 2025. Making Semgrep optional would remove it from the image. |
| certifi | runner image (all three Python environments), sandbox image | MPL-2.0 | Unmodified CA bundle. Approved for other CNCF projects; a blanket exception was recommended in February 2026. |
| pathspec | runner image | MPL-2.0 | Approved for another CNCF project. |
| tqdm | runner image, sandbox image | MPL-2.0 AND MIT | |
| orjson | runner image (`/opt/skillspector`), sandbox image | MPL-2.0 AND (Apache-2.0 OR MIT) | |
| regex | runner image (system and `/opt/skillspector`), sandbox image | Apache-2.0 AND CNRI-Python | |
| numpy, scipy | runner image (`/opt/skillspector`, numpy only), sandbox image | BSD-3-Clause for the packages; the wheels bundle libgfortran (GPL-3.0-or-later WITH GCC-exception-3.1), libquadmath (LGPL-2.1-or-later) and OpenBLAS (BSD-3-Clause); numpy also declares 0BSD, MIT, Zlib and CC0-1.0 parts | CC0-1.0, the GCC runtime exception and LGPL-2.1 are not on the allowlist. |
| pillow | sandbox image | MIT-CMU | |
| npm (bundled with Node.js 22.23.3) | sandbox image | Artistic-2.0 | Part of the Node.js distribution that nvm installs. |
| SkillEvaluator Unicode data | runner image, inside SkillEvaluator | Unicode-3.0 | `publication_text.py` contains Unicode 15.1 and 17.0.0 data (SkillEvaluator `THIRD_PARTY_NOTICES.md`). |
| Debian packages of the base images | all three images | mixed, including GPL-2, GPL-3.0-or-later, LGPL and MPL-2.0 | Operating system packages, see below. |

Claude Code in the sandbox image is proprietary. It is not an open source license, so it is not a case for the license exception process. See [Sandbox image](#sandbox-image).

## Controller

The controller is a single Go binary (`ghcr.io/dirien/skill-ci-controller`, linux/amd64 and linux/arm64) on the `gcr.io/distroless/static:nonroot` base image.

`go-licenses` found 83 third-party Go modules in the binary, all under allowlisted licenses or Apache-2.0:

| License | Modules |
| --- | --- |
| Apache-2.0 | 40 |
| BSD-3-Clause | 17 |
| Apache-2.0, with some packages under BSD-3-Clause (and MIT in `sigs.k8s.io/yaml`) | 15 |
| MIT | 10 |
| ISC | 1 |

The notable ones:

- Apache-2.0: `k8s.io/api`, `k8s.io/apimachinery`, `k8s.io/client-go`, `sigs.k8s.io/controller-runtime`, `github.com/prometheus/client_golang`, the OpenTelemetry Go SDK and OTLP exporters (`go.opentelemetry.io/otel/...`), `google.golang.org/grpc`, `oras.land/oras-go/v2`, `github.com/opencontainers/image-spec` and `go-digest`, `github.com/go-logr/logr`.
- BSD-3-Clause: `golang.org/x/net`, `x/oauth2`, `x/sync`, `x/sys`, `x/term`, `x/text`, `x/time`, `google.golang.org/protobuf`, `github.com/google/uuid`, `github.com/spf13/pflag`, `github.com/fsnotify/fsnotify`, `github.com/evanphx/json-patch/v5`, `github.com/grpc-ecosystem/grpc-gateway/v2`.
- MIT: `go.uber.org/zap`, `go.uber.org/multierr`, `go.yaml.in/yaml/v3`, `github.com/cespare/xxhash/v2`, `github.com/json-iterator/go`, `github.com/fxamacker/cbor/v2`, `github.com/emicklei/go-restful/v3`, `github.com/cenkalti/backoff/v5`, `github.com/beorn7/perks`, `github.com/x448/float16`.
- ISC: `github.com/davecgh/go-spew`.

The Go standard library compiled into the binary is BSD-3-Clause. The base image is built by the [distroless](https://github.com/GoogleContainerTools/distroless) project (Apache-2.0) and contains the Debian packages base-files (GPL-2+), ca-certificates (GPL-2+ and MPL-2.0), media-types, netbase (GPL-2) and tzdata (public domain).

## Reporting UI (npm)

The reporting site's single page app is built from `ui/` with Vite and embedded in the controller binary (`controller/internal/reportsite/web/dist/`). Checked on 2026-10-31 from the `license` field of each `node_modules/*/package.json` after `make ui-install` (`npm ci` from `ui/package-lock.json`). The lockfile has 211 entries; 186 were installed on linux/arm64. The other 25 are optional native binaries for other platforms, and their license comes from the lockfile: MIT for the 14 `@rolldown/binding-*` packages and `fsevents`, MPL-2.0 for the 10 `lightningcss-*` packages.

What ships: the built JavaScript holds only the runtime dependencies and their own dependencies, all MIT. Nothing else from `node_modules` reaches the controller image.

| Package | Version | License |
| --- | --- | --- |
| react | 19.3.1 | MIT |
| react-dom | 19.3.1 | MIT |
| react-router | 8.4.0 | MIT |
| scheduler (dependency of react-dom) | 0.28.0 | MIT |
| @remix-run/route-pattern (dependency of react-router) | 0.22.1 | MIT |
| cookie-es (dependency of react-router) | 3.1.1 | MIT |

Build and test tools (direct development dependencies; they run in CI and on a developer machine, they are not shipped):

| Package | Version | License |
| --- | --- | --- |
| vite | 8.3.4 | MIT |
| @vitejs/plugin-react | 6.1.2 | MIT |
| typescript | 6.0.3 | Apache-2.0 |
| vitest | 5.0.5 | MIT |
| jsdom | 30.1.3 | MIT |
| @testing-library/react | 16.3.3 | MIT |
| @testing-library/dom | 10.4.2 | MIT |
| eslint | 10.13.0 | MIT |
| @eslint/js | 10.0.1 | MIT |
| typescript-eslint | 8.71.2 | MIT |
| @types/react | 19.3.0 | MIT |
| @types/react-dom | 19.3.0 | MIT |

All 186 installed packages by license: MIT 144, Apache-2.0 17, BSD-2-Clause 8, ISC 7, BSD-3-Clause 3, MIT-0 2, MPL-2.0 2, BlueOak-1.0.0 2, CC0-1.0 1. The ones outside MIT, Apache-2.0, BSD and ISC are all transitive development dependencies:

| Package | Version | License | Pulled in by |
| --- | --- | --- | --- |
| lightningcss, lightningcss-linux-arm64-gnu (and the other platform binaries) | 1.33.0 | MPL-2.0 | vite (CSS minifier at build time) |
| lru-cache | 11.5.3 | BlueOak-1.0.0 | jsdom (test DOM) |
| minimatch | 10.2.6 | BlueOak-1.0.0 | eslint, typescript-eslint |
| mdn-data | 2.27.1 | CC0-1.0 | jsdom, through css-tree (CSS data) |
| @csstools/color-helpers, @csstools/css-syntax-patches-for-csstree | 6.1.2, 1.1.15 | MIT-0 | jsdom |

None of them is in the controller binary or any image. MPL-2.0 and CC0-1.0 are outside the CNCF allowlist (see the summary); check MIT-0 and BlueOak-1.0.0 against the allowlist before an application, in case the build tool chain counts.

## Runner image

`ghcr.io/dirien/skill-ci-runner` (linux/amd64) is built on `python:3.12.16-slim-bookworm`. Python itself is under the PSF license (PSF-2.0). The image has three Python environments, 260 distributions in total.

| Component | Version | How it is installed | License |
| --- | --- | --- | --- |
| [NVIDIA SkillEvaluator](https://github.com/NVIDIA/SkillEvaluator) | 0.6.0 | cloned at build time and patched with `skillevaluator-patch/`; its LICENSE, NOTICE and THIRD_PARTY_NOTICES.md are in `/opt/skillevaluator` | Apache-2.0 |
| [Harbor framework](https://github.com/harbor-framework/harbor) | 0.24.0 | PyPI, a dependency of `harbor-agent-sandbox` and SkillEvaluator | Apache-2.0 |
| harbor-agent-sandbox (this repository) | 0.1.0 | built from `harbor-agent-sandbox/` | Apache-2.0 |
| [NVIDIA SkillSpector](https://github.com/NVIDIA/SkillSpector) | 2.12.0 | pip from Git at a pinned commit, own virtualenv `/opt/skillspector` | Apache-2.0 |
| [Semgrep CE](https://github.com/semgrep/semgrep) | 1.183.0 | PyPI, own virtualenv `/opt/semgrep` | LGPL-2.1-or-later |
| Bandit, pip-audit | 1.9.4, 2.10.1 | PyPI | Apache-2.0 |
| kubernetes (Python client) | 37.0.1 | PyPI | Apache-2.0 |
| litellm | 1.93.2 | PyPI, a SkillEvaluator dependency | MIT |
| [ORAS](https://github.com/oras-project/oras) | 1.3.0 | release binary, checksum-verified | Apache-2.0 |
| [Gitleaks](https://github.com/gitleaks/gitleaks) | 8.30.1 | release binary, checksum-verified | MIT |

The 126 distributions in the system environment are almost all Apache-2.0, MIT, BSD-2-Clause, BSD-3-Clause, ISC or PSF-2.0. The exceptions are certifi, pathspec, tqdm and regex (see the summary). The 67 distributions in `/opt/skillspector` add orjson, regex, certifi and numpy (with its bundled libraries). The 67 in `/opt/semgrep` add Semgrep and certifi; peewee (MIT) and face (BSD-3-Clause) declare no license field and were checked from their license files; glom declares only the classifier "BSD License".

The Debian packages (125) include git (GPL-2), patch (GPL-3+), curl (curl license) and bash and coreutils (GPL-3+).

## Sandbox image

`ghcr.io/dirien/skill-ci-sandbox` (linux/amd64) is the image of every trial. It is built on `python:3.12.16-slim-bookworm` and contains:

| Component | Version | License |
| --- | --- | --- |
| Claude Code (Anthropic) | 2.1.292 | Proprietary. Anthropic's `LICENSE.md` reads "© Anthropic PBC. All rights reserved. Use is subject to Anthropic's Commercial Terms of Service." The native binary is installed by `sandbox/install-agents.sh` into `/root/.local/share/claude/versions/`. Anthropic's license terms apply to it. |
| [Codex CLI](https://github.com/openai/codex) (`@openai/codex` and `@openai/codex-linux-x64`) | 0.161.0 | Apache-2.0 |
| [OpenCode](https://github.com/sst/opencode) (`opencode-ai`) | 1.19.1 | MIT. The platform packages `opencode-linux-x64` and `opencode-linux-x64-baseline` have no license field; the upstream repository is MIT. |
| Node.js | 22.23.3 | MIT, with bundled components under their own licenses (Node.js `LICENSE`) |
| npm, corepack (bundled with Node.js) | 10.9.9, 0.36.0 | Artistic-2.0, MIT |
| [nvm](https://github.com/nvm-sh/nvm) | 0.40.2 | MIT |
| ragas and its dependencies (the verifier packages that SkillEvaluator's task Dockerfile installs) | ragas 0.4.3 | Apache-2.0, with 101 Python distributions in total |

The Python distributions are almost all Apache-2.0, MIT or BSD. The exceptions are certifi, orjson, tqdm, regex, numpy, scipy and pillow (see the summary). The Debian packages (130) include bash and coreutils (GPL-3+), git (GPL-2), procps (GPL-2+ and LGPL-2.1+), ripgrep (MIT or Unlicense), jq (MIT) and curl (curl license).

## Helm chart and OCI artifacts

The chart `oci://ghcr.io/dirien/charts/skill-ci-controller` is part of this repository (Apache-2.0) and declares no chart dependencies. The skills that users push to the registry are their own artifacts and carry their own licenses; the sample skills in [skills/](../skills/) are Apache-2.0.

## Installed alongside, not shipped

[deploy/install.sh](../deploy/install.sh) and the Pulumi stack in [infra/](../infra/) install these components from their upstream sources. skill-ci does not redistribute them.

| Component | Version | License |
| --- | --- | --- |
| [goharbor](https://github.com/goharbor/harbor) and its [Helm chart](https://github.com/goharbor/harbor-helm) | 2.15.4, chart 1.19.4 | Apache-2.0 |
| [kubernetes-sigs Agent Sandbox](https://github.com/kubernetes-sigs/agent-sandbox) | v1.0.9 | Apache-2.0 |
| [kube-prometheus-stack](https://github.com/prometheus-community/helm-charts) (optional) | chart 92.10.1 | Apache-2.0 for the chart and Prometheus; the Grafana it deploys is AGPL-3.0 |
| ORAS and curl toolbox images | v1.3.0, 8.14.1 | Apache-2.0, curl license |
| [Cilium](https://github.com/cilium/cilium) (optional, installed by the cluster operator) | | Apache-2.0 |

## Documentation

The documentation in this repository is covered by the Apache License 2.0 like the code. The CNCF charter licenses documentation of CNCF projects under CC-BY-4.0, so the documentation license would change on acceptance.
