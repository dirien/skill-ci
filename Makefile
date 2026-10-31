# skill-ci: infrastructure, build, deploy and demo targets. Run `make help`.
SHELL := /bin/bash
.DEFAULT_GOAL := help

# Where Pulumi gets its local backend and credentials. Skipped when the file does not exist.
PULUMI_ENV ?= $(HOME)/.pulumi-local/skill-ci-env.sh
PULUMI ?= pulumi

# Release that `make deploy` installs on the Pulumi stack: the stack config skillCiVersion (default in
# infra/Pulumi.yaml, the same as SKILL_CI_VERSION in deploy/versions.env). TAG=0.8.1 sets it first.

# Demo beats.
SKILL ?= acme-release-notes
VERSION ?= v1.1.0
MODE ?= add
# demo-model-swap: AGENT for MODE=require|optional, TO for MODE=bump (a Claude Code version).
AGENT ?=
TO ?=

# Helm chart.
CHART ?= charts/skill-ci-controller
HELM ?= helm
KUBECONFORM ?= kubeconform
CRD_CATALOG ?= https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json

# Optional lint tools.
SHELLCHECK ?= shellcheck
YAMLLINT ?= uvx yamllint
MARKDOWNLINT ?= npx --yes markdownlint-cli2@0.24.0
ACTIONLINT ?= actionlint
GORELEASER ?= goreleaser

# Reporting UI (ui/): Node at the release in ui/.nvmrc. SKILLCI_API=http://localhost:8082 makes
# ui-dev proxy a port-forwarded controller (make reports-ui) instead of serving the fixtures.
NPM ?= npm
SKILLCI_API ?=

.PHONY: help
help: ## List the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

.PHONY: up
up: ## Create or update the cluster, Harbor, monitoring and toolbox (pulumi up)
	@cd infra && { [ ! -f "$(PULUMI_ENV)" ] || . "$(PULUMI_ENV)"; } && $(PULUMI) up --yes

.PHONY: preview
preview: ## Show what pulumi up would change
	@cd infra && { [ ! -f "$(PULUMI_ENV)" ] || . "$(PULUMI_ENV)"; } && $(PULUMI) preview

.PHONY: deploy
deploy: ## Install a skill-ci release (OCI chart, CRD, GHCR images) with pulumi up (TAG=0.8.1 sets skillCiVersion)
	@demo/deploy.sh

.PHONY: seed
seed: ## Push the sample skills as v1.0.0 (RETRIGGER=1 re-fires the webhook for existing tags)
	@demo/seed.sh

.PHONY: demo-push
demo-push: ## Push a new skill version (SKILL=acme-release-notes VERSION=v1.1.0; v1.2.0 is a deliberate regression)
	@demo/demo-push.sh $(SKILL) $(VERSION)

.PHONY: demo-model-swap
demo-model-swap: ## Change the model list (MODE=add|swap|restore, bump [TO=2.1.308], require|optional AGENT=codex)
	@demo/model-swap.sh $(MODE) $(if $(filter bump,$(MODE)),$(TO),$(AGENT))

.PHONY: status
status: ## SkillEvals and the tags per skill in Harbor
	@demo/status.sh

.PHONY: report
report: ## Download the eval reports of a skill version from Harbor (SKILL=... VERSION=... [MODEL=...] OUT=reports/)
	@demo/report.sh $(SKILL) $(VERSION) "$(MODEL)" $(or $(OUT),reports)

.PHONY: report-page
report-page: ## Build the run report page with every SkillEval and the SkillEvaluator reports (OUT=reports/index.html)
	@demo/report-page.sh $(or $(OUT),reports/index.html)

.PHONY: dashboard
dashboard: ## Port-forward Grafana and print the login
	@demo/dashboard.sh

.PHONY: reports-ui
reports-ui: ## Port-forward the controller's reporting site and print its URL (PORT=8082)
	@demo/reports-ui.sh

.PHONY: reset
reset: ## Back to the start: delete SkillEvals and Jobs, reset tags, restore the model list
	@demo/reset.sh

.PHONY: down
down: ## Tear everything down. Not automated: it deletes the cluster.
	@echo "Refusing to run 'pulumi destroy' from make. It deletes the cluster, Harbor and the registry."
	@echo "If you really mean it: cd infra && . $(PULUMI_ENV) && pulumi destroy"
	@exit 1

.PHONY: check-skills
check-skills: ## Dataset-only check of the sample skills (no LLM calls)
	@demo/check-skills.sh

.PHONY: draft-evals
draft-evals: ## Draft evals/evals.json with SkillEvaluator for review, never the gate (SKILL=... [DIR=skills/<skill>] [FORCE=1]; one LLM call)
	@FORCE="$(FORCE)" demo/draft-evals.sh "$(if $(filter file,$(origin SKILL)),,$(SKILL))" "$(DIR)"

.PHONY: sandbox-image
sandbox-image: ## Build the generic sandbox image locally (needs docker; the release workflow publishes it)
	docker build -t skill-ci-sandbox:local sandbox

.PHONY: lint
lint: check-skills ## shellcheck, yamllint, markdownlint, JSON validity
	$(SHELLCHECK) demo/*.sh infra/harbor-setup.sh sandbox/*.sh
	$(YAMLLINT) -c .yamllint.yaml infra demo .github .goreleaser.yaml .yamllint.yaml .markdownlint.yaml
	$(MARKDOWNLINT) README.md DEMO.md ADOPTERS.md CHANGELOG.md CODE_OF_CONDUCT.md CONTRIBUTING.md GOVERNANCE.md MAINTAINERS.md ROADMAP.md SECURITY.md .github/PULL_REQUEST_TEMPLATE.md "docs/*.md" deploy/README.md ui/README.md "testdata/*/README.md" "skills/*/SKILL.md" "demo/variants/*/*/SKILL.md"
	python3 -m json.tool infra/dashboards/skill-ci.json >/dev/null
	@for f in skills/*/evals/evals.json demo/variants/*/*/evals/evals.json; do python3 -m json.tool "$$f" >/dev/null || exit 1; done

.PHONY: lint-workflows
lint-workflows: ## actionlint on .github/workflows (needs actionlint)
	$(ACTIONLINT)

.PHONY: ui-install
ui-install: ## Install the UI dependencies from ui/package-lock.json (npm ci)
	cd ui && $(NPM) ci

.PHONY: ui-build
ui-build: ## Build the UI into controller/internal/reportsite/web/dist, which go build embeds
	cd ui && $(NPM) run build

.PHONY: ui-test
ui-test: ## UI lint, typecheck and tests (eslint, tsc --noEmit, vitest)
	cd ui && $(NPM) run lint && $(NPM) run typecheck && $(NPM) run test

.PHONY: ui-dev
ui-dev: ## UI dev server on :5173 with the fixture API (SKILLCI_API=http://localhost:8082 proxies a controller)
	cd ui && SKILLCI_API="$(SKILLCI_API)" $(NPM) run dev

.PHONY: release-snapshot
release-snapshot: ## Dry-run the release: controller binaries, archives and image, no publish or signing (needs goreleaser, docker)
	$(GORELEASER) release --snapshot --clean --skip=publish,sign,sbom

.PHONY: chart-crds
chart-crds: ## Copy config/crd into the chart (crds/ is generated, never hand-edited)
	@mkdir -p $(CHART)/crds
	@rm -f $(CHART)/crds/*.yaml
	@cp config/crd/*.yaml $(CHART)/crds/

.PHONY: chart-lint
chart-lint: chart-crds ## helm lint --strict, template, kubeconform, yamllint, package (needs helm, kubeconform)
	$(HELM) lint --strict $(CHART)
	$(HELM) template skill-ci $(CHART) -n skill-ci --include-crds --set serviceMonitor.enabled=true | $(KUBECONFORM) -strict -summary -ignore-missing-schemas -schema-location default -schema-location '$(CRD_CATALOG)' -
	$(YAMLLINT) -c .yamllint.yaml $(CHART)/Chart.yaml $(CHART)/values.yaml
	@d="$$(mktemp -d)" && $(HELM) package $(CHART) -d "$$d" >/dev/null && $(HELM) show chart "$$d"/*.tgz >/dev/null && rm -rf "$$d"
