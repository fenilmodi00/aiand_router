#!/usr/bin/env bash
set -euo pipefail
printf ' /force-parent %s\n' "${AIAND_BUILD_COORDINATOR_MODEL:-z-ai/glm-5.3}"
