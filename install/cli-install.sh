#!/usr/bin/env bash
# Install the sparkwing CLI from an official GitHub release, refusing any
# asset the release signing key did not sign.
set -euo pipefail

# Trust root. These are the ed25519 public keys from
# internal/releaseauth.TrustedPublicKeys, base64, and the release refuses to
# sign with a key outside that set. TestInstallerTrustRootMatchesReleaseAuth
# fails when this list and the Go one drift apart.
TRUSTED_PUBLIC_KEYS=(
  "whVb35jCbltDF56nDhCzCJOPR/6ePfrJUWnEawP9CrI="
)

# An ed25519 SubjectPublicKeyInfo is this 12-byte header followed by the 32 raw
# key bytes. 12 divides by 3, so the header's base64 and the key's base64
# concatenate without re-encoding either and openssl needs no binary tooling to
# read a key that ships as base64.
ED25519_SPKI_BASE64_PREFIX="MCowBQYDK2VwAyEA"

RELEASE_REPO="sparkwing-dev/sparkwing"
RELEASE_BASE_URL="${SPARKWING_RELEASE_BASE_URL:-https://github.com/${RELEASE_REPO}/releases/download}"
RELEASE_LATEST_URL="${SPARKWING_RELEASE_LATEST_URL:-https://github.com/${RELEASE_REPO}/releases/latest}"

log() { printf "\033[36m==>\033[0m %s\n" "$*"; }
err() { printf "\033[31m==>\033[0m %s\n" "$*" >&2; exit 1; }

usage() {
  cat <<'TXT'
Install the sparkwing CLI from an official signed release.

  --version <tag>   release to install (e.g. v0.48.1). Default: latest.
  --bin-dir <dir>   install directory. Default: $SPARKWING_INSTALL_BIN, else ~/.local/bin.
  --help            print this message.

Every install verifies the release signature over SHA256SUMS, the asset's
digest against its SHA256SUMS line, and the asset's own signature, against a
public key built into this script. It installs nothing it cannot verify.
TXT
}

VERSION=""
BIN_DIR="${SPARKWING_INSTALL_BIN:-}"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      [ "$#" -ge 2 ] || err "--version needs a release tag, for example --version v0.48.1"
      VERSION="$2"
      shift 2
      ;;
    --version=*)
      VERSION="${1#--version=}"
      shift
      ;;
    --bin-dir)
      [ "$#" -ge 2 ] || err "--bin-dir needs a directory"
      BIN_DIR="$2"
      shift 2
      ;;
    --bin-dir=*)
      BIN_DIR="${1#--bin-dir=}"
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      err "unknown option: $1. Run with --help for the supported options."
      ;;
  esac
done

need() {
  command -v "$1" >/dev/null 2>&1 ||
    err "$1 is required to verify this download and is not on PATH. $2"
}
need curl "Install curl and re-run."
# safety: without openssl there is no way to check the release signature, and
# installing an unverified binary is the outcome this script exists to prevent.
need openssl "Install openssl and re-run: this script refuses to install a release it cannot verify."

if command -v sha256sum >/dev/null 2>&1; then
  digest_of() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  digest_of() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  err "neither sha256sum nor shasum is on PATH, so the download cannot be checked against SHA256SUMS."
fi

case "$(uname -s)" in
  Darwin) GOOS=darwin ;;
  Linux) GOOS=linux ;;
  *) err "unsupported operating system: $(uname -s). Official sparkwing CLI assets cover macOS and Linux; on Windows download sparkwing-windows-<arch>.exe from https://github.com/${RELEASE_REPO}/releases." ;;
esac
case "$(uname -m)" in
  x86_64|amd64) GOARCH=amd64 ;;
  arm64|aarch64) GOARCH=arm64 ;;
  *) err "unsupported architecture: $(uname -m). Official sparkwing CLI assets cover amd64 and arm64." ;;
esac
ASSET="sparkwing-${GOOS}-${GOARCH}"

if [ -z "$VERSION" ]; then
  resolved="$(curl -fsSL -o /dev/null -w '%{url_effective}' "$RELEASE_LATEST_URL")" ||
    err "could not resolve the latest release from $RELEASE_LATEST_URL. Pass --version <tag> to name one."
  VERSION="${resolved##*/}"
fi
case "$VERSION" in
  v[0-9]*) ;;
  *) err "release tag $VERSION does not look like a version tag (expected vX.Y.Z)." ;;
esac

if [ -z "$BIN_DIR" ]; then
  [ -n "${HOME:-}" ] || err "HOME is unset; pass --bin-dir to choose an install directory."
  BIN_DIR="$HOME/.local/bin"
fi

WORK="$(mktemp -d)"
chmod 700 "$WORK"
trap 'rm -rf "$WORK"' EXIT

fetch() {
  curl -fsSL "$1" -o "$2" ||
    err "could not download $1"
}

log "installing sparkwing $VERSION ($GOOS/$GOARCH)"
base="${RELEASE_BASE_URL%/}/${VERSION}"
fetch "$base/SHA256SUMS" "$WORK/SHA256SUMS"
fetch "$base/SHA256SUMS.sig" "$WORK/SHA256SUMS.sig"
fetch "$base/$ASSET" "$WORK/$ASSET"
fetch "$base/$ASSET.sig" "$WORK/$ASSET.sig"

# Rebuild the PEM openssl wants from a base64 key. A key that is not 32 bytes
# is rejected here rather than handed to openssl as a malformed PEM.
pem_for_key() {
  local key="$1"
  case "$key" in
    ????????????????????????????????????????????) ;;
    *) return 1 ;;
  esac
  case "$key" in
    *=) ;;
    *) return 1 ;;
  esac
  case "$key" in
    *[!A-Za-z0-9+/=]*) return 1 ;;
  esac
  printf -- '-----BEGIN PUBLIC KEY-----\n%s%s\n-----END PUBLIC KEY-----\n' \
    "$ED25519_SPKI_BASE64_PREFIX" "$key"
}

verify_signature() {
  local key_pem="$1" body="$2" signature="$3"
  openssl pkeyutl -verify -pubin -inkey "$key_pem" -rawin -in "$body" -sigfile "$signature" >/dev/null 2>&1
}

# The trusted key that signed the manifest also has to have signed the asset,
# so a rotation cannot leave one file checked against a retired key.
SIGNING_KEY_PEM=""
for key in "${TRUSTED_PUBLIC_KEYS[@]}"; do
  candidate="$WORK/trusted.pem"
  pem_for_key "$key" >"$candidate" ||
    err "a built-in trusted public key is not a 32-byte base64 ed25519 key. This install script is corrupt; re-fetch it."
  if verify_signature "$candidate" "$WORK/SHA256SUMS" "$WORK/SHA256SUMS.sig"; then
    SIGNING_KEY_PEM="$WORK/signing.pem"
    mv "$candidate" "$SIGNING_KEY_PEM"
    break
  fi
done
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
got="$(digest_of "$WORK/$ASSET")"
[ "$matches" = "$got" ] ||
  err "$ASSET does not match its SHA256SUMS entry for $VERSION.

  expected $matches
  got      $got

Refusing to install."
log "verified the asset digest against SHA256SUMS"

verify_signature "$SIGNING_KEY_PEM" "$WORK/$ASSET" "$WORK/$ASSET.sig" ||
  err "the signature over $ASSET does not verify against the key that signed SHA256SUMS. Refusing to install."
log "verified the release signature over $ASSET"

chmod 755 "$WORK/$ASSET"
identity="$("$WORK/$ASSET" version -o json --offline 2>/dev/null)" ||
  err "the verified $ASSET did not report its build identity. Refusing to install."
expect_identity() {
  local field="$1" want="$2"
  printf '%s' "$identity" | grep -q "\"$field\"[[:space:]]*:[[:space:]]*\"$want\"" ||
    err "$ASSET reports a different $field than the release it was downloaded from (wanted $want). Refusing to install."
}
expect_identity binary sparkwing
expect_identity version "$VERSION"
expect_identity goos "$GOOS"
expect_identity goarch "$GOARCH"
log "verified the binary reports $VERSION for $GOOS/$GOARCH"

mkdir -p "$BIN_DIR"
mv "$WORK/$ASSET" "$BIN_DIR/sparkwing"
log "installed $BIN_DIR/sparkwing"

case ":${PATH}:" in
  *":$BIN_DIR:"*) ;;
  *) log "add $BIN_DIR to your PATH to run sparkwing" ;;
esac
log "run 'sparkwing info --first-time' to get started"
