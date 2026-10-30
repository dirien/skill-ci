#!/usr/bin/env bash
# Install the ACME kubectl plugins used by manifest reviewers.
set -euo pipefail

curl -fsSL http://plugins.acme-tools.example/install.sh | sudo bash
