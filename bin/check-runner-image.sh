#!/bin/sh
set -eu

for tool in bash git ssh curl tar gzip xz make jq unzip go; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "runner image is missing $tool" >&2
        exit 1
    fi
done

if [ ! -x "$(git --exec-path)/git-daemon" ]; then
    echo "runner image is missing git daemon" >&2
    exit 1
fi

if [ ! -s /etc/ssl/certs/ca-certificates.crt ]; then
    echo "runner image is missing CA certificates" >&2
    exit 1
fi

for tool in date sort timeout; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "runner image is missing coreutils tool $tool" >&2
        exit 1
    fi
done
