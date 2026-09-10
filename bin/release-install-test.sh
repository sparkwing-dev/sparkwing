#!/usr/bin/env bash
# Exercises install/install.sh against a staged release whose asset is a real
# sparkwing build, stamped the way the release workflow stamps one. The fixture
# comes from the product, so a probe that reads a field the CLI does not print
# fails the good case instead of passing on a hand-written stub.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

TAG="v9.9.9"
OTHER_TAG="v0.0.1"
failures=0

note() { printf '  %s\n' "$*"; }

case_failed() {
  printf 'FAIL %s: %s\n' "$1" "$2" >&2
  failures=$((failures + 1))
}

if ! command -v openssl >/dev/null 2>&1; then
  echo "release-install-test: openssl is required" >&2
  exit 1
fi
if ! openssl genpkey -algorithm ed25519 -out "$WORK/signing.pem" 2>/dev/null; then
  echo "release-install-test: this openssl cannot generate ed25519 keys" >&2
  exit 1
fi
openssl genpkey -algorithm ed25519 -out "$WORK/other.pem" 2>/dev/null
raw_public_key() {
  openssl pkey -in "$1" -pubout -outform DER | tail -c 32 | openssl base64 -A
}
SIGNING_KEY_B64="$(raw_public_key "$WORK/signing.pem")"

sign() { openssl pkeyutl -sign -inkey "$1" -rawin -in "$2" -out "$3"; }

GOOS="$(GOWORK=off go -C "$ROOT" env GOOS)"
GOARCH="$(GOWORK=off go -C "$ROOT" env GOARCH)"
ASSET="sparkwing-${GOOS}-${GOARCH}"

# The one fixture the whole suite runs on: the real CLI, stamped like a release.
note "building $ASSET from ./cmd/sparkwing stamped $TAG"
GOWORK=off go -C "$ROOT" build -trimpath -ldflags "-s -w -X main.Version=$TAG" \
  -o "$WORK/$ASSET" ./cmd/sparkwing
reported="$("$WORK/$ASSET" version -o plain --offline)"
if [ "$reported" != "$TAG" ]; then
  echo "release-install-test: the staged build reports $reported, not $TAG" >&2
  exit 1
fi

# stage_release <dir> <tag>: a complete, correctly signed release. Cases mutate
# a copy of it, so every refusal is one difference away from an install.
stage_release() {
  stage_dir="$1/$2"
  mkdir -p "$stage_dir"
  cp "$WORK/$ASSET" "$stage_dir/$ASSET"
  (cd "$stage_dir" && sha256sum "$ASSET" >SHA256SUMS)
  sign "$WORK/signing.pem" "$stage_dir/SHA256SUMS" "$stage_dir/SHA256SUMS.sig"
  sign "$WORK/signing.pem" "$stage_dir/$ASSET" "$stage_dir/$ASSET.sig"
}

# The installer trusts the release key; the staged release is signed by a
# throwaway one, so the suite swaps the trust root and nothing else.
INSTALLER="$WORK/install.sh"
sed "s|^TRUSTED_PUBLIC_KEYS=.*|TRUSTED_PUBLIC_KEYS=\"$SIGNING_KEY_B64\"|" \
  "$ROOT/install/install.sh" >"$INSTALLER"
chmod +x "$INSTALLER"
if ! grep -q "$SIGNING_KEY_B64" "$INSTALLER"; then
  echo "release-install-test: could not replace the installer trust root" >&2
  exit 1
fi

# run_install <name> <releases-dir> <tag> [env=value ...]: runs the installer
# against a staged release and records stdout, stderr and the exit status.
run_install() {
  run_name="$1"
  releases="$2"
  run_tag="$3"
  shift 3
  run_prefix="$WORK/prefix-$run_name"
  rm -rf "$run_prefix"
  run_home="$WORK/home-$run_name"
  mkdir -p "$run_home"
  set +e
  env "$@" \
    HOME="$run_home" SPARKWING_HOME="$run_home/.sparkwing" \
    SPARKWING_RELEASE_BASE_URL="file://$releases" \
    "${SHELL_UNDER_TEST:-sh}" "$INSTALLER" --version "$run_tag" --prefix "$run_prefix" \
    >"$WORK/$run_name.out" 2>"$WORK/$run_name.err"
  run_status=$?
  set -e
}

refuses() {
  refuse_name="$1"
  refuse_message="$2"
  if [ "$run_status" -eq 0 ]; then
    case_failed "$refuse_name" "installed a release it should have refused"
    return
  fi
  if ! grep -qF "$refuse_message" "$WORK/$refuse_name.err"; then
    case_failed "$refuse_name" "refusal does not say $refuse_message: $(tr '\n' ' ' <"$WORK/$refuse_name.err")"
    return
  fi
  if [ -e "$WORK/prefix-$refuse_name/sparkwing" ]; then
    case_failed "$refuse_name" "left a binary behind after refusing"
  fi
}

# A correctly signed release installs, and the installed binary is the one the
# tag names. This is the case a probe reading a field the CLI never prints
# cannot pass.
GOOD="$WORK/releases-good"
stage_release "$GOOD" "$TAG"
run_install good "$GOOD" "$TAG"
if [ "$run_status" -ne 0 ]; then
  case_failed good "refused a correctly signed release: $(tr '\n' ' ' <"$WORK/good.err")"
elif [ "$("$WORK/prefix-good/sparkwing" version -o plain --offline)" != "$TAG" ]; then
  case_failed good "installed a binary that does not report $TAG"
fi

# The same release under dash and bash: the site tells adopters to pipe this
# script into sh, so a bashism is a broken install on every Debian derivative.
for shell_under_test in sh bash; do
  if ! command -v "$shell_under_test" >/dev/null 2>&1; then
    continue
  fi
  SHELL_UNDER_TEST="$shell_under_test" run_install "shell-$shell_under_test" "$GOOD" "$TAG"
  if [ "$run_status" -ne 0 ]; then
    case_failed "shell-$shell_under_test" "failed under $shell_under_test: $(tr '\n' ' ' <"$WORK/shell-$shell_under_test.err")"
  fi
done

TAMPERED="$WORK/releases-tampered"
stage_release "$TAMPERED" "$TAG"
printf 'malicious' >>"$TAMPERED/$TAG/$ASSET"
run_install tampered-asset "$TAMPERED" "$TAG"
refuses tampered-asset "does not match its SHA256SUMS entry"

MISSING_MANIFEST_SIG="$WORK/releases-missing-manifest-sig"
stage_release "$MISSING_MANIFEST_SIG" "$TAG"
rm "$MISSING_MANIFEST_SIG/$TAG/SHA256SUMS.sig"
run_install missing-manifest-signature "$MISSING_MANIFEST_SIG" "$TAG"
refuses missing-manifest-signature "SHA256SUMS.sig"

MALFORMED_SIG="$WORK/releases-malformed-sig"
stage_release "$MALFORMED_SIG" "$TAG"
printf 'not a signature' >"$MALFORMED_SIG/$TAG/SHA256SUMS.sig"
run_install malformed-manifest-signature "$MALFORMED_SIG" "$TAG"
refuses malformed-manifest-signature "does not verify against any key this installer trusts"

WRONG_MANIFEST_KEY="$WORK/releases-wrong-manifest-key"
stage_release "$WRONG_MANIFEST_KEY" "$TAG"
sign "$WORK/other.pem" "$WRONG_MANIFEST_KEY/$TAG/SHA256SUMS" "$WRONG_MANIFEST_KEY/$TAG/SHA256SUMS.sig"
run_install wrong-key-manifest-signature "$WRONG_MANIFEST_KEY" "$TAG"
refuses wrong-key-manifest-signature "does not verify against any key this installer trusts"

WRONG_ASSET_KEY="$WORK/releases-wrong-asset-key"
stage_release "$WRONG_ASSET_KEY" "$TAG"
sign "$WORK/other.pem" "$WRONG_ASSET_KEY/$TAG/$ASSET" "$WRONG_ASSET_KEY/$TAG/$ASSET.sig"
run_install wrong-key-asset-signature "$WRONG_ASSET_KEY" "$TAG"
refuses wrong-key-asset-signature "does not verify against the key that signed SHA256SUMS"

MISSING_ASSET_SIG="$WORK/releases-missing-asset-sig"
stage_release "$MISSING_ASSET_SIG" "$TAG"
rm "$MISSING_ASSET_SIG/$TAG/$ASSET.sig"
run_install missing-asset-signature "$MISSING_ASSET_SIG" "$TAG"
refuses missing-asset-signature "$ASSET.sig"

printf 'decoy' >"$WORK/decoy"
decoy_digest="$(sha256sum "$WORK/decoy" | cut -d' ' -f1)"

# A signed manifest that lists other assets but not this platform's.
ABSENT_LINE="$WORK/releases-absent-line"
stage_release "$ABSENT_LINE" "$TAG"
printf '%s  sparkwing-openbsd-riscv64\n' "$decoy_digest" >"$ABSENT_LINE/$TAG/SHA256SUMS"
sign "$WORK/signing.pem" "$ABSENT_LINE/$TAG/SHA256SUMS" "$ABSENT_LINE/$TAG/SHA256SUMS.sig"
run_install absent-checksum-line "$ABSENT_LINE" "$TAG"
refuses absent-checksum-line "has no line for $ASSET"

# A signed manifest that names the asset with another file's digest: the
# installer must read the line for the asset, not the first line it can parse.
SUBSTITUTED_LINE="$WORK/releases-substituted-line"
stage_release "$SUBSTITUTED_LINE" "$TAG"
printf '%s  %s\n' "$decoy_digest" "$ASSET" >"$SUBSTITUTED_LINE/$TAG/SHA256SUMS"
sign "$WORK/signing.pem" "$SUBSTITUTED_LINE/$TAG/SHA256SUMS" "$SUBSTITUTED_LINE/$TAG/SHA256SUMS.sig"
run_install substituted-checksum-line "$SUBSTITUTED_LINE" "$TAG"
refuses substituted-checksum-line "does not match its SHA256SUMS entry"

DUPLICATE_LINE="$WORK/releases-duplicate-line"
stage_release "$DUPLICATE_LINE" "$TAG"
cat "$DUPLICATE_LINE/$TAG/SHA256SUMS" "$DUPLICATE_LINE/$TAG/SHA256SUMS" >"$WORK/duplicated"
mv "$WORK/duplicated" "$DUPLICATE_LINE/$TAG/SHA256SUMS"
sign "$WORK/signing.pem" "$DUPLICATE_LINE/$TAG/SHA256SUMS" "$DUPLICATE_LINE/$TAG/SHA256SUMS.sig"
run_install duplicated-checksum-line "$DUPLICATE_LINE" "$TAG"
refuses duplicated-checksum-line "more than once"

# A genuine, correctly signed release republished under another tag. Signatures
# cover bytes, so only the binary's own report catches the substitution.
SUBSTITUTED_TAG="$WORK/releases-substituted-tag"
stage_release "$SUBSTITUTED_TAG" "$OTHER_TAG"
run_install substituted-release-tag "$SUBSTITUTED_TAG" "$OTHER_TAG"
refuses substituted-release-tag "not $OTHER_TAG"

# No openssl that can verify an ed25519 signature: the refusal must name the
# missing capability, because reporting a tool gap as tampering sends the
# adopter looking for an attacker.
NO_OPENSSL="$WORK/no-openssl"
mkdir -p "$NO_OPENSSL"
run_install missing-openssl "$GOOD" "$TAG" "SPARKWING_OPENSSL=$NO_OPENSSL/openssl"
refuses missing-openssl "which arrived in OpenSSL 3.0"
if grep -qF "tampered" "$WORK/missing-openssl.err"; then
  case_failed missing-openssl "reported a missing openssl as tampering"
fi

# An openssl that exits 0 for everything must not pass for a verifier: the
# capability probe requires the vector's signature to be refused over other bytes.
PERMISSIVE="$WORK/permissive"
mkdir -p "$PERMISSIVE"
cat >"$PERMISSIVE/openssl" <<'STUB'
#!/usr/bin/env sh
exit 0
STUB
chmod +x "$PERMISSIVE/openssl"
run_install permissive-openssl "$GOOD" "$TAG" "SPARKWING_OPENSSL=$PERMISSIVE/openssl"
refuses permissive-openssl "which arrived in OpenSSL 3.0"

if command -v shellcheck >/dev/null 2>&1; then
  if ! shellcheck --severity=warning --shell=sh "$ROOT/install/install.sh"; then
    case_failed posix-shell "install/install.sh is not clean under shellcheck --shell=sh"
  fi
else
  note "shellcheck not on PATH; skipped the POSIX shell check"
fi

if [ "$failures" -ne 0 ]; then
  printf 'release-install-test: %d case(s) failed\n' "$failures" >&2
  exit 1
fi
echo "release-install-test: install/install.sh verified a staged release and refused every mutation"
