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

func TestLoadBootstrapAdminToken(t *testing.T) {
	const token = "swu_provisionedbootstrapadmin00001"

	t.Run("file", func(t *testing.T) {
		path := writeFile(t, "token", token+"\n")
		got, err := loadBootstrapAdminToken(path)
		if err != nil {
			t.Fatalf("loadBootstrapAdminToken: %v", err)
		}
		if got != token {
			t.Fatalf("token = %q, want %q", got, token)
		}
	})

	t.Run("environment wins over the file", func(t *testing.T) {
		t.Setenv("SPARKWING_BOOTSTRAP_ADMIN_TOKEN", token)
		got, err := loadBootstrapAdminToken(writeFile(t, "token", "swu_ignoredfilevalue0000000000001"))
		if err != nil {
			t.Fatalf("loadBootstrapAdminToken: %v", err)
		}
		if got != token {
			t.Fatalf("token = %q, want the environment value", got)
		}
	})

	t.Run("unset", func(t *testing.T) {
		got, err := loadBootstrapAdminToken("")
		if err != nil {
			t.Fatalf("loadBootstrapAdminToken: %v", err)
		}
		if got != "" {
			t.Fatalf("token = %q, want empty", got)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		if _, err := loadBootstrapAdminToken(writeFile(t, "token", "  \n")); err == nil {
			t.Fatal("loadBootstrapAdminToken accepted an empty file")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if _, err := loadBootstrapAdminToken(filepath.Join(t.TempDir(), "absent")); err == nil {
			t.Fatal("loadBootstrapAdminToken accepted a path that does not exist")
		}
	})
}

func TestLoadSecretsCipher_PreviousKey(t *testing.T) {
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	previous, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	encode := base64.StdEncoding.EncodeToString

	t.Run("no keys", func(t *testing.T) {
		c, err := loadSecretsCipher("", "")
		if err != nil {
			t.Fatalf("loadSecretsCipher: %v", err)
		}
		if c != nil {
			t.Fatal("loadSecretsCipher built a cipher with no key configured")
		}
	})

	t.Run("current only", func(t *testing.T) {
		c, err := loadSecretsCipher(writeFile(t, "key", encode(key)), "")
		if err != nil {
			t.Fatalf("loadSecretsCipher: %v", err)
		}
		if c == nil {
			t.Fatal("loadSecretsCipher returned no cipher for a configured key")
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
		c, err := loadSecretsCipher(writeFile(t, "key", encode(key)), writeFile(t, "previous", encode(previous)))
		if err != nil {
			t.Fatalf("loadSecretsCipher: %v", err)
		}
		got, err := c.Open(sealed)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if got != "supersecret" {
			t.Fatalf("Open = %q, want supersecret", got)
		}
	})

	t.Run("previous without a current key", func(t *testing.T) {
		_, err := loadSecretsCipher("", writeFile(t, "previous", encode(previous)))
		if err == nil {
			t.Fatal("loadSecretsCipher accepted a previous key with no current one")
		}
		if !strings.Contains(err.Error(), "previous secrets key") {
			t.Fatalf("err = %v, want it to name the previous key", err)
		}
	})

	t.Run("raw key bytes", func(t *testing.T) {
		c, err := loadSecretsCipher(writeFile(t, "key", string(key)), "")
		if err != nil {
			t.Fatalf("loadSecretsCipher: %v", err)
		}
		if c == nil {
			t.Fatal("loadSecretsCipher returned no cipher for a raw 32-byte key file")
		}
	})
}
