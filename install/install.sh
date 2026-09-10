#!/usr/bin/env sh
# Sparkwing installer.
#
#   curl -fsSL https://sparkwing.dev/install.sh | sh
#   curl -fsSL https://sparkwing.dev/install.sh | sh -s -- --version v0.49.0
#   curl -fsSL https://sparkwing.dev/install.sh | sh -s -- --prefix /usr/local/bin
#
# Every install checks the release signature over SHA256SUMS, the asset's
# digest against its SHA256SUMS line, and the asset's own signature, against a
# public key built into this script. It installs nothing it cannot verify.
set -eu

# Trust root: the ed25519 public keys from internal/releaseauth.TrustedPublicKeys,
# base64. The release refuses to sign with a key outside that set, and
# TestInstallerTrustRootMatchesReleaseAuth fails when the two copies drift.
TRUSTED_PUBLIC_KEYS="whVb35jCbltDF56nDhCzCJOPR/6ePfrJUWnEawP9CrI="

# An ed25519 SubjectPublicKeyInfo is this 12-byte header followed by the 32 raw
# key bytes. 12 divides by 3, so the header's base64 and the key's base64
# concatenate without re-encoding either, and openssl reads a key that ships as
# base64 without any binary tooling.
ED25519_SPKI_BASE64_PREFIX="MCowBQYDK2VwAyEA"

# RFC 8032 test vector 2: the one-byte message `r`, its signature, and the
# public key that signed it. An openssl that verifies this vector and rejects
# the same signature over other bytes can check a release signature.
ED25519_VECTOR_KEY="PUAXw+hDiVqStwqnTRt+vJyYLM8uxJaMwM1V8Sr0Zgw="
ED25519_VECTOR_SIG="kqAJqfDUyrhyDoILX2QlQKKye1QWUD+Ps3YiI+vbadoIWsHkPhWZbkWPNhPQ8R2MOHsurrQwKu6wDSkWErsMAA=="
ED25519_VECTOR_MESSAGE="r"

REPO="${SPARKWING_REPO:-sparkwing-dev/sparkwing}"
RELEASE_BASE_URL="${SPARKWING_RELEASE_BASE_URL:-https://github.com/${REPO}/releases/download}"
RELEASE_LATEST_URL="${SPARKWING_RELEASE_LATEST_URL:-https://github.com/${REPO}/releases/latest}"

PREFIX=""
VERSION=""

log() { printf '\033[36m==>\033[0m %s\n' "$*"; }
err() { printf '\033[31m==>\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<EOF
Usage: install.sh [--version vX.Y.Z] [--prefix DIR]

  --version  Pin a specific release tag. Default: latest.
  --prefix   Install directory. Default: \$HOME/.local/bin.

Environment:
  SPARKWING_REPO     Override owner/repo (used for staging tests).
  SPARKWING_OPENSSL  Path to an openssl that can verify ed25519 signatures.
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version) [ $# -ge 2 ] || err "--version needs a release tag, for example --version v0.49.0"; VERSION="$2"; shift 2 ;;
    --version=*) VERSION="${1#--version=}"; shift ;;
    --prefix) [ $# -ge 2 ] || err "--prefix needs a directory"; PREFIX="$2"; shift 2 ;;
    --prefix=*) PREFIX="${1#--prefix=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) printf 'install.sh: unknown flag: %s\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
done

command -v curl >/dev/null 2>&1 || err "curl is required to download a release and is not on PATH."

WORK="$(mktemp -d 2>/dev/null || mktemp -d -t sparkwing-install)"
chmod 700 "$WORK"
trap 'rm -rf "$WORK"' EXIT INT TERM

# Rebuilds the PEM openssl wants from a base64 key, rejecting anything that is
# not 32 bytes rather than handing openssl a malformed PEM.
pem_for_key() {
  case "$1" in
    ????????????????????????????????????????????) ;;
    *) return 1 ;;
  esac
  case "$1" in
    *=) ;;
    *) return 1 ;;
  esac
  case "$1" in
    *[!A-Za-z0-9+/=]*) return 1 ;;
  esac
  printf -- '-----BEGIN PUBLIC KEY-----\n%s%s\n-----END PUBLIC KEY-----\n' "$ED25519_SPKI_BASE64_PREFIX" "$1"
}

verify_signature() {
  "$OPENSSL" pkeyutl -verify -pubin -inkey "$1" -rawin -in "$2" -sigfile "$3" >/dev/null 2>&1
}

# `openssl pkeyutl -rawin` arrived in OpenSSL 3.0. macOS ships LibreSSL and
# older distributions ship 1.1.1, so the capability is probed with a known
# vector instead of a version string: a candidate must both accept the vector
# and reject the same signature over other bytes, so a tool that ignores the
# flags it does not know cannot pass for a verifier.
openssl_verifies_ed25519() {
  command -v "$OPENSSL" >/dev/null 2>&1 || return 1
  pem_for_key "$ED25519_VECTOR_KEY" >"$WORK/vector.pem" || return 1
  printf '%s' "$ED25519_VECTOR_SIG" | "$OPENSSL" base64 -d -A >"$WORK/vector.sig" 2>/dev/null || return 1
  printf '%s' "$ED25519_VECTOR_MESSAGE" >"$WORK/vector.msg"
  verify_signature "$WORK/vector.pem" "$WORK/vector.msg" "$WORK/vector.sig" || return 1
  printf 'tampered' >"$WORK/vector.tampered"
  if verify_signature "$WORK/vector.pem" "$WORK/vector.tampered" "$WORK/vector.sig"; then
    return 1
  fi
  return 0
}

# SPARKWING_OPENSSL names the binary to use and nothing else is tried, so a
# wrong answer is visible rather than silently repaired from PATH.
if [ -n "${SPARKWING_OPENSSL:-}" ]; then
  set -- "$SPARKWING_OPENSSL"
else
  set -- openssl \
    /opt/homebrew/opt/openssl@3/bin/openssl \
    /usr/local/opt/openssl@3/bin/openssl \
    /opt/homebrew/bin/openssl \
    /usr/local/bin/openssl
fi
OPENSSL=""
for candidate in "$@"; do
  OPENSSL="$candidate"
  if openssl_verifies_ed25519; then
    break
  fi
  OPENSSL=""
done
[ -n "$OPENSSL" ] || err "no openssl on this system can verify an ed25519 signature, so the release cannot be checked.

This is not a signature failure: nothing was verified. Checking a Sparkwing
release needs \`openssl pkeyutl -rawin\`, which arrived in OpenSSL 3.0; macOS
ships LibreSSL and older distributions ship OpenSSL 1.1.1, and neither has it.

  macOS:         brew install openssl@3
  Debian/Ubuntu: apt-get install openssl   (3.0 or newer)

Then re-run, or point SPARKWING_OPENSSL at an OpenSSL 3 binary."

digest_of() {
  digest_line="$("$OPENSSL" dgst -sha256 "$1")" || return 1
  # OpenSSL 3 prints `SHA2-256(path)= <digest>`, older builds `SHA256(path)= <digest>`.
  printf '%s\n' "$digest_line" | awk '{print $NF}'
}

case "$(uname -s)" in
  Darwin) GOOS=darwin ;;
  Linux) GOOS=linux ;;
  MINGW*|MSYS*|CYGWIN*) GOOS=windows ;;
  *) err "unsupported operating system: $(uname -s). Official sparkwing assets cover macOS, Linux, and Windows under Git Bash." ;;
esac
case "$(uname -m)" in
  x86_64|amd64) GOARCH=amd64 ;;
  arm64|aarch64) GOARCH=arm64 ;;
  *) err "unsupported architecture: $(uname -m). Official sparkwing assets cover amd64 and arm64." ;;
esac
EXT=""
if [ "$GOOS" = windows ]; then
  EXT=".exe"
fi
ASSET="sparkwing-${GOOS}-${GOARCH}${EXT}"

if [ -z "$VERSION" ]; then
  resolved="$(curl -fsSL -o /dev/null -w '%{url_effective}' "$RELEASE_LATEST_URL")" ||
    err "could not resolve the latest release from $RELEASE_LATEST_URL. Pass --version <tag> to name one."
  VERSION="${resolved##*/}"
fi
case "$VERSION" in
  v[0-9]*) ;;
  *) err "release tag $VERSION does not look like a version tag (expected vX.Y.Z)." ;;
esac

if [ -z "$PREFIX" ]; then
  [ -n "${HOME:-}" ] || err "HOME is unset; pass --prefix to choose an install directory."
  PREFIX="$HOME/.local/bin"
fi

fetch() { curl -fsSL "$1" -o "$2" || err "could not download $1"; }

log "installing sparkwing $VERSION ($GOOS/$GOARCH)"
base="${RELEASE_BASE_URL%/}/${VERSION}"
fetch "$base/SHA256SUMS" "$WORK/SHA256SUMS"
fetch "$base/SHA256SUMS.sig" "$WORK/SHA256SUMS.sig"
fetch "$base/$ASSET" "$WORK/$ASSET"
fetch "$base/$ASSET.sig" "$WORK/$ASSET.sig"

# The key that signed the manifest must also have signed the asset, so a
# rotation cannot leave one file checked against a retired key.
SIGNING_KEY_PEM=""
printf '%s\n' "$TRUSTED_PUBLIC_KEYS" >"$WORK/trusted.keys"
while IFS= read -r key; do
  [ -n "$key" ] || continue
  pem_for_key "$key" >"$WORK/candidate.pem" ||
    err "a built-in trusted public key is not a 32-byte base64 ed25519 key. This copy of the install script is corrupt; re-fetch it."
  if verify_signature "$WORK/candidate.pem" "$WORK/SHA256SUMS" "$WORK/SHA256SUMS.sig"; then
    SIGNING_KEY_PEM="$WORK/signing.pem"
    mv "$WORK/candidate.pem" "$SIGNING_KEY_PEM"
    break
  fi
done <"$WORK/trusted.keys"
[ -n "$SIGNING_KEY_PEM" ] ||
  err "the signature over SHA256SUMS for $VERSION does not verify against any key this installer trusts.

Refusing to install. Either the download was tampered with, or the release
signing key was rotated after this copy of the install script was written. A
fresh copy carries the current key:

  curl -fsSL https://sparkwing.dev/install.sh | sh -s -- --version $VERSION"
log "verified the release signature over SHA256SUMS"

matches="$(awk -v want="$ASSET" '$2 == want || $2 == "*" want { print $1 }' "$WORK/SHA256SUMS")"
[ -n "$matches" ] ||
  err "SHA256SUMS for $VERSION has no line for $ASSET, so the download cannot be checked. Refusing to install."
[ "$(printf '%s\n' "$matches" | wc -l)" -eq 1 ] ||
  err "SHA256SUMS for $VERSION lists $ASSET more than once. Refusing to install."
got="$(digest_of "$WORK/$ASSET")" || err "openssl could not hash the download. Refusing to install."
[ "$matches" = "$got" ] ||
  err "$ASSET does not match its SHA256SUMS entry for $VERSION.

  expected $matches
  got      $got

Refusing to install."
log "verified the asset digest against SHA256SUMS"

verify_signature "$SIGNING_KEY_PEM" "$WORK/$ASSET" "$WORK/$ASSET.sig" ||
  err "the signature over $ASSET does not verify against the key that signed SHA256SUMS. Refusing to install."
log "verified the release signature over $ASSET"

# A signature covers bytes, not a tag, so an older signed release served under
# a newer tag would pass every check above. The binary's own report is what
# catches that substitution.
chmod 755 "$WORK/$ASSET"
identity="$("$WORK/$ASSET" version -o json --offline 2>/dev/null)" ||
  err "the verified $ASSET did not report its version. Refusing to install."
case "$identity" in
  *"\"installed\":\"$VERSION\""*) ;;
  *) err "$ASSET is a signed sparkwing release, but not $VERSION: it reports a different version.

Refusing to install. A release asset served under another release's tag is
either a mistake in the release or a downgrade attempt." ;;
esac
log "verified the binary reports $VERSION"

mkdir -p "$PREFIX"
# `install` replaces the binary atomically, so a running sparkwing survives.
install -m 0755 "$WORK/$ASSET" "$PREFIX/sparkwing$EXT"
log "installed $PREFIX/sparkwing$EXT"

case ":${PATH:-}:" in
  *":$PREFIX:"*) ;;
  *)
    printf '\n'
    printf 'Note: %s is not on your PATH. Add it to your shell rc:\n' "$PREFIX"
    printf '  export PATH="%s:$PATH"\n\n' "$PREFIX"
    ;;
esac

# Absolute path: the user's shell has not rehashed PATH yet.
log "running: sparkwing info --first-time"
printf '\n'
"$PREFIX/sparkwing$EXT" info --first-time
