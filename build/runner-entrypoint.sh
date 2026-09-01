#!/bin/sh
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
