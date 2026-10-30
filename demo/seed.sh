#!/usr/bin/env bash
# Push every sample skill from skills/ as v1.0.0 (existing tags are skipped).
#   demo/seed.sh              push what is missing
#   RETRIGGER=1 demo/seed.sh  also re-fire the push webhook for v1.0.0 tags that already exist
set -euo pipefail
# shellcheck source=demo/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

need kubectl
wait_toolbox

say "Seeding the sample skills as v1.0.0"
found=0
for dir in "${ROOT_DIR}"/skills/*/; do
  [[ -f "${dir}SKILL.md" ]] || continue
  found=1
  skill="$(basename "${dir}")"
  existing=""
  if [[ "${RETRIGGER:-0}" == "1" ]]; then
    existing="$(tag_digest "${skill}" v1.0.0)"
  fi
  if [[ -n "${existing}" ]]; then
    "${DEMO_DIR}/retrigger.sh" "${skill}" v1.0.0
  else
    "${DEMO_DIR}/push-skill.sh" "${skill}" v1.0.0 "${dir%/}"
  fi
done
[[ "${found}" == "1" ]] || die "no skills found under ${ROOT_DIR}/skills"

say "Done. Tags in Harbor:"
"${DEMO_DIR}/status.sh" --tags-only
