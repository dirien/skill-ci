# skill-ci: infrastructure, build, deploy and demo targets. Run `make help`.
SHELL := /bin/bash
.DEFAULT_GOAL := help

# Where Pulumi gets its local backend and credentials. Skipped when the file does not exist.
PULUMI_ENV ?= $(HOME)/.pulumi-local/skill-ci-env.sh
PULUMI ?= pulumi

# Image build: in-cluster BuildKit through a port-forward. DOCKER_CONFIG must hold DOCR push credentials.
BUILDCTL ?= buildctl
DOCR_DOCKER_CONFIG ?= $(HOME)/.cache/skill-ci/docker
DOCKER_CONFIG ?= $(if $(wildcard $(DOCR_DOCKER_CONFIG)/config.json),$(DOCR_DOCKER_CONFIG),$(HOME)/.docker)
CONTROLLER_TAG ?= 0.4.1
RUNNER_TAG ?= 0.5.0
REGISTRY ?= registry.digitalocean.com/skill-ci-dirien

# Demo beats.
SKILL ?= acme-release-notes
VERSION ?= v1.1.0
MODE ?= add

# Helm chart.
CHART ?= charts/skill-ci-controller
HELM ?= helm
KUBECONFORM ?= kubeconform
CRD_CATALOG ?= https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json

# Optional lint tools.
SHELLCHECK ?= shellcheck
YAMLLINT ?= uvx yamllint
MARKDOWNLINT ?= npx --yes markdownlint-cli2

export BUILDCTL DOCKER_CONFIG DOCR_DOCKER_CONFIG CONTROLLER_TAG RUNNER_TAG REGISTRY

.PHONY: help
help: ## List the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

.PHONY: up
up: ## Create or update the cluster, Harbor, monitoring and toolbox (pulumi up)
	@cd infra && { [ ! -f "$(PULUMI_ENV)" ] || . "$(PULUMI_ENV)"; } && $(PULUMI) up --yes

.PHONY: preview
preview: ## Show what pulumi up would change
	@cd infra && { [ ! -f "$(PULUMI_ENV)" ] || . "$(PULUMI_ENV)"; } && $(PULUMI) preview

.PHONY: docr-login
docr-login: ## Write DOCR push credentials (from the docr secret) to ~/.cache/skill-ci/docker
	@demo/docr-login.sh

.PHONY: build
build: ## Build and push the controller and runner images with the in-cluster BuildKit
	@demo/build.sh all

.PHONY: deploy
deploy: ## Install the controller with its Helm chart (adopts a kubectl apply -k install)
	@demo/deploy.sh

.PHONY: seed
seed: ## Push the sample skills as v1.0.0 (RETRIGGER=1 re-fires the webhook for existing tags)
	@demo/seed.sh

.PHONY: demo-push
demo-push: ## Push a new skill version (SKILL=acme-release-notes VERSION=v1.1.0; v1.2.0 is a deliberate regression)
	@demo/demo-push.sh $(SKILL) $(VERSION)

.PHONY: demo-model-swap
demo-model-swap: ## Change the model list (MODE=add|swap|restore)
	@demo/model-swap.sh $(MODE)

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

.PHONY: lint
lint: check-skills ## shellcheck, yamllint, markdownlint, JSON validity
	$(SHELLCHECK) demo/*.sh infra/harbor-setup.sh
	$(YAMLLINT) -c .yamllint.yaml infra demo .yamllint.yaml .markdownlint.yaml
	$(MARKDOWNLINT) README.md DEMO.md "skills/*/SKILL.md" "demo/variants/*/*/SKILL.md"
	python3 -m json.tool infra/dashboards/skill-ci.json >/dev/null
	@for f in skills/*/evals/evals.json demo/variants/*/*/evals/evals.json; do python3 -m json.tool "$$f" >/dev/null || exit 1; done

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
