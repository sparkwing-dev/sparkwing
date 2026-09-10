package releaseauth

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"regexp"
	"strings"
	"testing"
)

const installerPath = "../../install/cli-install.sh"

var (
	installerKeyBlock = regexp.MustCompile(`(?s)TRUSTED_PUBLIC_KEYS=\((.*?)\n\)`)
	installerKeyLine  = regexp.MustCompile(`"([^"]+)"`)
	installerPrefix   = regexp.MustCompile(`ED25519_SPKI_BASE64_PREFIX="([^"]+)"`)
)

func readInstaller(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(installerPath)
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}
	return string(body)
}

func installerTrustedKeys(t *testing.T) []string {
	t.Helper()
	block := installerKeyBlock.FindStringSubmatch(readInstaller(t))
	if block == nil {
		t.Fatalf("%s has no TRUSTED_PUBLIC_KEYS array; the installer trust root moved", installerPath)
	}
	var keys []string
	for _, match := range installerKeyLine.FindAllStringSubmatch(block[1], -1) {
		keys = append(keys, match[1])
	}
	return keys
}

// The installer cannot import this package, so its trust root is a second copy
// of TrustedPublicKeys. This is what stops the copies drifting apart.
func TestInstallerTrustRootMatchesReleaseAuth(t *testing.T) {
	got := installerTrustedKeys(t)
	if len(got) != len(TrustedPublicKeys) {
		t.Fatalf("%s trusts %d key(s), releaseauth trusts %d; update both together",
			installerPath, len(got), len(TrustedPublicKeys))
	}
	for i, want := range TrustedPublicKeys {
		if got[i] != want {
			t.Fatalf("%s trusted key %d is %q, releaseauth has %q; update both together",
				installerPath, i, got[i], want)
		}
	}
}

// The installer builds the PEM openssl reads by pasting its prefix in front of
// a base64 key, so a wrong prefix would verify releases against a wrong key.
func TestInstallerPrefixRebuildsTheTrustedKeys(t *testing.T) {
	match := installerPrefix.FindStringSubmatch(readInstaller(t))
	if match == nil {
		t.Fatalf("%s has no ED25519_SPKI_BASE64_PREFIX", installerPath)
	}
	prefix := match[1]

	for _, encoded := range installerTrustedKeys(t) {
		want, err := PublicKey(encoded)
		if err != nil {
			t.Fatalf("installer key %q: %v", encoded, err)
		}
		body := "-----BEGIN PUBLIC KEY-----\n" + prefix + encoded + "\n-----END PUBLIC KEY-----\n"
		block, _ := pem.Decode([]byte(body))
		if block == nil {
			t.Fatalf("installer key %q does not splice into a PEM block", encoded)
		}
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			t.Fatalf("installer key %q does not splice into a public key: %v", encoded, err)
		}
		got, ok := parsed.(ed25519.PublicKey)
		if !ok {
			t.Fatalf("installer key %q splices into %T, want ed25519", encoded, parsed)
		}
		if !got.Equal(want) {
			t.Fatalf("installer key %q splices into a different key than releaseauth trusts", encoded)
		}
	}
}

// A trust root only an operator can read is one nobody checks, so the value in
// the installer has to be the printable form the release tooling reports.
func TestInstallerKeysAreBase64Encoded(t *testing.T) {
	for _, encoded := range installerTrustedKeys(t) {
		if strings.ContainsAny(encoded, " \t") {
			t.Fatalf("installer key %q carries whitespace", encoded)
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatalf("installer key %q is not base64: %v", encoded, err)
		}
		if len(raw) != ed25519.PublicKeySize {
			t.Fatalf("installer key %q decodes to %d bytes, want %d", encoded, len(raw), ed25519.PublicKeySize)
		}
	}
}
