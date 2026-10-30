#!/usr/bin/env bash
# Point a copy of the chart at the images of one release, for the chart pushed to GHCR.
#   .github/scripts/chart-release-values.sh <chart-dir> <version>
# The chart in the repository keeps its DOCR defaults, which the demo cluster uses (make deploy).
# The published chart defaults to ghcr.io/dirien/skill-ci-controller (the appVersion tag) and
# ghcr.io/dirien/skill-ci-runner:<version>, and drops the docr pull secret. Edited with sed, not
# yq, so the comments and blank lines of values.yaml survive. Fails if a DOCR default is left.
set -euo pipefail

chart="${1:?chart directory}"
version="${2:?version without leading v}"
values="${chart}/values.yaml"
docr="registry.digitalocean.com/skill-ci-dirien"

sed -i \
  -e "s#${docr}/skill-ci-controller#ghcr.io/dirien/skill-ci-controller#" \
  -e "s#${docr}/skill-ci-runner#ghcr.io/dirien/skill-ci-runner#" \
  -e "/^  runnerImage:/,/^    tag:/ s#^    tag: .*#    tag: \"${version}\"#" \
  -e '/^imagePullSecrets:/,/^$/c\imagePullSecrets: []\n' \
  "${values}"

if grep -n 'digitalocean\|name: docr' "${values}"; then
  echo "error: ${values} still has a DOCR default" >&2
  exit 1
fi
grep -q "^    tag: \"${version}\"" "${values}" || { echo "error: runner image tag not set" >&2; exit 1; }
