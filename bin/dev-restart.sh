#!/usr/bin/env bash
# Stop + start the dashboard dev loop. Convenient after re-installing
# sparkwing, since next dev's hot reload covers UI changes but Go-side
# changes still require a fresh `sparkwing dashboard` supervisor.

set -uo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
bash "$REPO/bin/dev-stop.sh"
exec bash "$REPO/bin/dev-start.sh"
