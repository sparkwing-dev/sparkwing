package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func credentialsDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestReadCredentialsTrimsTokensAndLeavesAbsentFilesOff(t *testing.T) {
	const token = "swu_provisionedbootstrapadmin00001"
	creds, err := readCredentials(credentialsDir(t, map[string]string{
		credBootstrapAdminToken: token + "\n",
		credPGURL:               "postgres://sparkwing@db/sparkwing\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if creds.BootstrapAdminToken != token || creds.PGURL != "postgres://sparkwing@db/sparkwing" {
		t.Fatalf("credentials = %+v, want the trimmed token and URL", creds)
	}
	if creds.License != "" || creds.SecretsKey != nil || creds.OIDCKey != nil {
		t.Fatalf("credentials = %+v, want every absent file read as off", creds)
	}
	if _, err := readCredentials(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("readCredentials accepted a directory that does not exist")
	}
	if creds, err := readCredentials(""); err != nil || creds.PGURL != "" {
		t.Fatalf("readCredentials(\"\") = %+v, %v; want nothing configured", creds, err)
	}
}

func TestReadCredentialsRefusesAnEmptyPGURL(t *testing.T) {
	for _, body := range []string{"", " \n\t"} {
		_, err := readCredentials(credentialsDir(t, map[string]string{credPGURL: body}))
		if err == nil || !strings.Contains(err.Error(), credPGURL) {
			t.Fatalf("pg-url %q: error = %v, want a refusal naming %s rather than a fall back to SQLite", body, err, credPGURL)
		}
	}
}

func TestSecretsKeyCredentials(t *testing.T) {
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	previous, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	encode := base64.StdEncoding.EncodeToString
	cipherFrom := func(t *testing.T, files map[string]string) (*secrets.Cipher, error) {
		t.Helper()
		creds, err := readCredentials(credentialsDir(t, files))
		if err != nil {
			return nil, err
		}
		return secretsCipher(creds.SecretsKey, creds.SecretsPreviousKey)
	}

	t.Run("no keys", func(t *testing.T) {
		c, err := cipherFrom(t, nil)
		if err != nil || c != nil {
			t.Fatalf("cipher = %v, %v; want none with no key configured", c, err)
		}
	})

	t.Run("previous opens an older envelope", func(t *testing.T) {
		old, err := secrets.NewCipher(previous)
		if err != nil {
			t.Fatalf("NewCipher: %v", err)
		}
		sealed, err := old.Seal("supersecret")
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		c, err := cipherFrom(t, map[string]string{credSecretsKey: encode(key) + "\n", credSecretsPreviousKey: encode(previous)})
		if err != nil {
			t.Fatalf("cipher: %v", err)
		}
		if got, err := c.Open(sealed); err != nil || got != "supersecret" {
			t.Fatalf("Open = %q, %v; want supersecret", got, err)
		}
	})

	t.Run("previous without a current key", func(t *testing.T) {
		_, err := cipherFrom(t, map[string]string{credSecretsPreviousKey: encode(previous)})
		if err == nil || !strings.Contains(err.Error(), credSecretsPreviousKey) {
			t.Fatalf("err = %v, want a refusal naming %s", err, credSecretsPreviousKey)
		}
	})

	t.Run("raw key bytes", func(t *testing.T) {
		c, err := cipherFrom(t, map[string]string{credSecretsKey: string(key)})
		if err != nil || c == nil {
			t.Fatalf("cipher = %v, %v; want one for a raw 32-byte key file", c, err)
		}
	})

	t.Run("malformed key", func(t *testing.T) {
		if _, err := cipherFrom(t, map[string]string{credSecretsKey: "not a key"}); err == nil ||
			!strings.Contains(err.Error(), credSecretsKey) {
			t.Fatalf("err = %v, want a refusal naming %s", err, credSecretsKey)
		}
	})
}
