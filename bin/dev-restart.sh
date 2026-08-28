#!/usr/bin/env bash

set -uo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
bash "$REPO/bin/dev-stop.sh"
exec bash "$REPO/bin/dev-start.sh"
