#!/usr/bin/env bash
# Stage a release under file:// and prove install/cli-install.sh installs the
# good one and refuses every corrupted one.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CASE_ROOT="$(mktemp -d)"
trap 'rm -rf "$CASE_ROOT"' EXIT

command -v openssl >/dev/null 2>&1 || {
  echo "cli-install-test: openssl is required" >&2
  exit 1
}

case "$(uname -s)" in
  Darwin) GOOS=darwin ;;
  Linux) GOOS=linux ;;
  *)
    echo "cli-install-test: unsupported host $(uname -s)" >&2
    exit 1
    ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) GOARCH=amd64 ;;
  arm64 | aarch64) GOARCH=arm64 ;;
  *)
    echo "cli-install-test: unsupported host $(uname -m)" >&2
    exit 1
    ;;
esac
ASSET="sparkwing-${GOOS}-${GOARCH}"
# A second asset the release also covers, so a manifest missing our line is
# still a well-formed manifest.
DECOY="sparkwing-runner-${GOOS}-${GOARCH}"
VERSION="v9.9.9"

if command -v sha256sum >/dev/null 2>&1; then
  digest_of() { sha256sum "$1" | cut -d' ' -f1; }
else
  digest_of() { shasum -a 256 "$1" | cut -d' ' -f1; }
fi

# Throwaway keys, generated per run. No key material is ever committed.
KEYS="$CASE_ROOT/keys"
mkdir -p "$KEYS"
for name in release attacker; do
  openssl genpkey -algorithm ed25519 -out "$KEYS/$name.pem" 2>/dev/null
  openssl pkey -in "$KEYS/$name.pem" -pubout -outform DER |
    tail -c 32 | base64 | tr -d '\n' >"$KEYS/$name.pub"
done

# The shipped script carries its trust root as a literal, on purpose: no
# environment variable may replace it. The test edits a copy instead.
SCRIPT="$CASE_ROOT/cli-install.sh"
sed "s|\"whVb35jCbltDF56nDhCzCJOPR/6ePfrJUWnEawP9CrI=\"|\"$(cat "$KEYS/release.pub")\"|" \
  "$ROOT/install/cli-install.sh" >"$SCRIPT"
grep -q "$(cat "$KEYS/release.pub")" "$SCRIPT" ||
  { echo "cli-install-test: could not substitute the trusted key; has the literal moved?" >&2; exit 1; }

fail() {
  echo "cli-install-test: $1" >&2
  shift
  for f in "$@"; do
    [ -f "$f" ] && sed 's/^/    /' "$f" >&2
  done
  exit 1
}

# stage <dir> [reported-version]: a complete, correctly signed release.
stage() {
  local dist="$1/$VERSION" reported="${2:-$VERSION}"
  mkdir -p "$dist"
  cat >"$dist/$ASSET" <<EOF
#!/bin/sh
printf '{"binary":"sparkwing","version":"%s","modified":false,"goos":"%s","goarch":"%s"}\n' \\
  "$reported" "$GOOS" "$GOARCH"
EOF
  chmod 755 "$dist/$ASSET"
  # A real SHA256SUMS covers every asset in the release, so the fixture does
  # too: dropping one line has to leave a manifest that still parses.
  printf 'decoy\n' >"$dist/$DECOY"
  {
    printf '%s  %s\n' "$(digest_of "$dist/$ASSET")" "$ASSET"
    printf '%s  %s\n' "$(digest_of "$dist/$DECOY")" "$DECOY"
  } >"$dist/SHA256SUMS"
  openssl pkeyutl -sign -inkey "$KEYS/release.pem" -rawin \
    -in "$dist/SHA256SUMS" -out "$dist/SHA256SUMS.sig"
  openssl pkeyutl -sign -inkey "$KEYS/release.pem" -rawin \
    -in "$dist/$ASSET" -out "$dist/$ASSET.sig"
}

run_install() {
  local root="$1" home="$2"
  mkdir -p "$home"
  env SPARKWING_RELEASE_BASE_URL="file://$root" HOME="$home" \
    bash "$SCRIPT" --version "$VERSION" --bin-dir "$home/bin"
}

# refuses <case> <mutate-fn> [expected-message]
refuses() {
  local name="$1" mutate="$2" expect="${3:-}"
  local root="$CASE_ROOT/$name" home="$CASE_ROOT/$name-home"
  stage "$root"
  "$mutate" "$root/$VERSION"
  if run_install "$root" "$home" >"$CASE_ROOT/$name.out" 2>&1; then
    fail "installer accepted $name" "$CASE_ROOT/$name.out"
  fi
  [ ! -e "$home/bin/sparkwing" ] ||
    fail "installer left a binary behind after refusing $name" "$CASE_ROOT/$name.out"
  if [ -n "$expect" ]; then
    grep -q "$expect" "$CASE_ROOT/$name.out" ||
      fail "refusing $name did not explain why (wanted: $expect)" "$CASE_ROOT/$name.out"
  fi
  echo "  refused: $name"
}

tamper_asset() { printf 'malicious\n' >>"$1/$ASSET"; }
drop_manifest_signature() { rm -f "$1/SHA256SUMS.sig"; }
corrupt_manifest_signature() { printf 'not a signature' >"$1/SHA256SUMS.sig"; }
wrong_key_manifest() {
  openssl pkeyutl -sign -inkey "$KEYS/attacker.pem" -rawin \
    -in "$1/SHA256SUMS" -out "$1/SHA256SUMS.sig"
}
wrong_key_asset() {
  openssl pkeyutl -sign -inkey "$KEYS/attacker.pem" -rawin \
    -in "$1/$ASSET" -out "$1/$ASSET.sig"
}
drop_checksum_line() {
  grep -v " $ASSET\$" "$1/SHA256SUMS" >"$1/SHA256SUMS.trimmed"
  mv "$1/SHA256SUMS.trimmed" "$1/SHA256SUMS"
  resign_manifest "$1"
}
resign_manifest() {
  openssl pkeyutl -sign -inkey "$KEYS/release.pem" -rawin \
    -in "$1/SHA256SUMS" -out "$1/SHA256SUMS.sig"
}
# A signed manifest that covers a different asset: the attacker controls the
# manifest but cannot make it name the asset being installed.
substitute_checksum_line() {
  printf '%s  %s\n' "$(digest_of "$1/$ASSET")" "$DECOY" >"$1/SHA256SUMS"
  resign_manifest "$1"
}
duplicate_checksum_line() {
  grep " $ASSET\$" "$1/SHA256SUMS" >>"$1/SHA256SUMS.dup"
  cat "$1/SHA256SUMS.dup" >>"$1/SHA256SUMS"
  rm -f "$1/SHA256SUMS.dup"
  resign_manifest "$1"
}
drop_asset_signature() { rm -f "$1/$ASSET.sig"; }

echo "negative cases:"
refuses tampered-asset tamper_asset "does not match its SHA256SUMS entry"
refuses missing-manifest-signature drop_manifest_signature "could not download"
refuses malformed-manifest-signature corrupt_manifest_signature "does not verify against any key"
refuses wrong-key-manifest wrong_key_manifest "does not verify against any key"
refuses wrong-key-asset wrong_key_asset "does not verify against the key that signed SHA256SUMS"
refuses missing-asset-signature drop_asset_signature "could not download"
refuses absent-checksum-line drop_checksum_line "has no line for"
refuses substituted-checksum-line substitute_checksum_line "has no line for"
refuses duplicated-checksum-line duplicate_checksum_line "more than once"

# A release whose binary reports a version other than the tag it was fetched
# under is a mismatch the signature alone cannot catch.
root="$CASE_ROOT/version-mismatch"
home="$CASE_ROOT/version-mismatch-home"
stage "$root" "v0.0.1"
if run_install "$root" "$home" >"$CASE_ROOT/version-mismatch.out" 2>&1; then
  fail "installer accepted a binary reporting the wrong version" "$CASE_ROOT/version-mismatch.out"
fi
[ ! -e "$home/bin/sparkwing" ] ||
  fail "installer left a binary behind after a version mismatch" "$CASE_ROOT/version-mismatch.out"
grep -q "reports a different version" "$CASE_ROOT/version-mismatch.out" ||
  fail "version mismatch did not explain why" "$CASE_ROOT/version-mismatch.out"
echo "  refused: version-mismatch"

# Without openssl there is no verification to perform, so the install must not
# happen at all.
root="$CASE_ROOT/no-openssl"
home="$CASE_ROOT/no-openssl-home"
stage "$root"
STUB="$CASE_ROOT/stub-bin"
mkdir -p "$STUB"
for tool in bash curl sha256sum shasum uname mktemp chmod mkdir mv rm awk cut wc grep head sed tr cat; do
  path="$(command -v "$tool" 2>/dev/null || true)"
  [ -n "$path" ] && ln -sf "$path" "$STUB/$tool"
done
mkdir -p "$home"
if env -i PATH="$STUB" HOME="$home" SPARKWING_RELEASE_BASE_URL="file://$root" \
  bash "$SCRIPT" --version "$VERSION" --bin-dir "$home/bin" \
  >"$CASE_ROOT/no-openssl.out" 2>&1; then
  fail "installer ran to completion without openssl" "$CASE_ROOT/no-openssl.out"
fi
[ ! -e "$home/bin/sparkwing" ] ||
  fail "installer left a binary behind without openssl" "$CASE_ROOT/no-openssl.out"
grep -q "openssl is required" "$CASE_ROOT/no-openssl.out" ||
  fail "missing openssl was not named" "$CASE_ROOT/no-openssl.out"
echo "  refused: missing-openssl"

echo "positive case:"
root="$CASE_ROOT/good"
home="$CASE_ROOT/good-home"
stage "$root"
if ! run_install "$root" "$home" >"$CASE_ROOT/good.out" 2>&1; then
  fail "installer refused a correctly signed release" "$CASE_ROOT/good.out"
fi
[ -x "$home/bin/sparkwing" ] ||
  fail "verified release was not installed" "$CASE_ROOT/good.out"
"$home/bin/sparkwing" version -o json --offline | grep -q '"version":"v9.9.9"' ||
  fail "installed binary is not the staged one" "$CASE_ROOT/good.out"
for marker in "verified the release signature over SHA256SUMS" \
  "verified the asset digest against SHA256SUMS" \
  "verified the release signature over $ASSET" \
  "verified the binary reports"; do
  grep -q "$marker" "$CASE_ROOT/good.out" ||
    fail "a successful install did not report: $marker" "$CASE_ROOT/good.out"
done
echo "  accepted: correctly signed release"

echo "cli-install-test: ok"
