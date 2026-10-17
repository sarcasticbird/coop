#!/bin/bash
# Stage checked artifacts only. No service/profile changes or VM starts.
set -euo pipefail
exec /usr/bin/python3 "$(dirname "$0")/install_agent_worker.py" "$@"
