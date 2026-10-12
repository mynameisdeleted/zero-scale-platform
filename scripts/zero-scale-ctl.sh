#!/usr/bin/env bash
# ==============================================================================
# Zero-Scale Platform Management CLI Wrapper
# Forwards to native Go implementation in cmd/zero-scale-ctl
# ==============================================================================
set -eo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

if [ -f "${ROOT_DIR}/zero-scale-ctl" ]; then
  exec "${ROOT_DIR}/zero-scale-ctl" "$@"
else
  exec go run "${ROOT_DIR}/cmd/zero-scale-ctl" "$@"
fi
