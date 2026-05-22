#!/bin/sh
# Runtime wrapper for the sparkwing-runner pod. Seeds ~/.netrc from
# GITHUB_TOKEN so the runner can clone private consumer repos and
# `go mod download` private dependencies over HTTPS, then exec's
# whatever was passed as the command (typically sparkwing-runner).
#
# Empty / unset GITHUB_TOKEN is fine. Public consumer repos work
# without it; private repos surface a clear "fatal: Authentication
# failed" from git when the token is missing rather than failing
# silently in some other layer.
set -eu

netrc="${HOME:-/tmp}/.netrc"

if [ -n "${GITHUB_TOKEN:-}" ]; then
    umask 077
    cat >"$netrc" <<EOF
machine github.com
login x-access-token
password $GITHUB_TOKEN
EOF
fi

exec "$@"
