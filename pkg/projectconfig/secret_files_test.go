package projectconfig

import (
	"strings"
	"testing"
)

func TestRepositorySecretFileAllowances(t *testing.T) {
	cfg, err := Parse([]byte("allow_secret_files: [.gitleaks.toml, fixtures/example.key]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowSecretFiles) != 2 || cfg.AllowSecretFiles[1] != "fixtures/example.key" {
		t.Fatalf("allowances lost: %+v", cfg)
	}
	for _, path := range []string{"../outside", "/etc/passwd", ".git/config", "nested/.GIT/config", ":(glob)*", "a/../secret", "a\\secret", "", "./example.key"} {
		if _, err := Parse([]byte("allow_secret_files: ['" + path + "']\n")); err == nil || !strings.Contains(err.Error(), "allow_secret_files") {
			t.Fatalf("unsafe allowance %q: %v", path, err)
		}
	}
}
