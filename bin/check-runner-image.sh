#!/bin/sh
set -eu

# safety: the controller schedules tool:<name> nodes onto this image from
# this list, so a listed tool the image lacks fails the build.
grep -v '^#' "${RUNNER_TOOLS:-/usr/local/share/sparkwing/runner-tools}" | while read -r tool; do
    [ -z "$tool" ] && continue
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "runner image declares $tool in build/runner-tools but lacks it" >&2
        exit 1
    fi
done

# safety: cache misses compile pipelines with this Go, so an image for one
# architecture must not carry the other's toolchain.
case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) arch=$(uname -m) ;;
esac
if command -v go >/dev/null 2>&1; then
    go_arch=$(go env GOHOSTARCH)
    if [ "$go_arch" != "$arch" ]; then
        echo "runner image for $arch bundles a $go_arch Go toolchain" >&2
        exit 1
    fi
fi

for tool in bash git ssh curl tar gzip xz make jq unzip go nc nsenter; do
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
