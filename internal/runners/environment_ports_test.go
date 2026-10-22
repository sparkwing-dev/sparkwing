package runners

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestRunnerInstallEnvironmentUsesSharedPolicy(t *testing.T) {
	const secret = "long-operator-value-0123"
	ambient := []string{"PATH=/runner/bin", "HTTPS_PROXY=http://user:proxy-pass@proxy.corp:3128", "no_proxy=localhost", "NODE_EXTRA_CA_CERTS=/etc/corp-ca.pem", "SSL_CERT_FILE=/etc/corp-ca.pem", "GITHUB_TOKEN=runner-github", "NODE_OPTIONS=--require=/tmp/x.js", "npm_config_cache=/runner/npm", "GOPROXY=https://registry.example/go", "npm_config_registry=https://registry.example/npm", "PIP_INDEX_URL=https://registry.example/pypi", "PIP_TRUSTED_HOST=registry.example", "REGISTRY_NOTE=" + secret, "AWS_REGION=us-west-2"}
	for _, listed := range []bool{false, true} {
		t.Run(fmt.Sprint(listed), func(t *testing.T) {
			masker := secrets.NewMasker()
			base := procenv.Base{Ambient: ambient, Masker: masker}
			if listed {
				base.SubmitEnvAllow = []string{"GOPROXY", "npm_config_registry", "PIP_INDEX_URL", "PIP_TRUSTED_HOST", "REGISTRY_NOTE", "AWS_REGION"}
			}
			env, err := procenv.Build(procenv.Install, base, procenv.Runtime{})
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range ambient[:5] {
				if !slices.Contains(slices.Collect(env.Values()), entry) {
					t.Fatalf("missing remote base %q", entry)
				}
			}
			for _, entry := range ambient[5:8] {
				if slices.Contains(slices.Collect(env.Values()), entry) {
					t.Fatalf("inherited unlisted control or credential %q", entry)
				}
			}
			for _, entry := range ambient[8:] {
				if slices.Contains(slices.Collect(env.Values()), entry) != listed {
					t.Fatalf("allow-list verdict changed for %q", entry)
				}
			}
			for _, value := range []string{"user:proxy-pass", "proxy-pass"} {
				if strings.Contains(masker.Mask(value), value) {
					t.Fatal("proxy credentials remained visible")
				}
			}
			if listed && (strings.Contains(masker.Mask(secret), secret) || masker.Mask("us-west-2") != "us-west-2") {
				t.Fatal("operator masking changed")
			}
		})
	}
	if _, err := procenv.Build(procenv.Install, procenv.Base{SubmitEnvAllow: []string{"*"}}, procenv.Runtime{}); err == nil {
		t.Fatal("bare wildcard accepted")
	}
}

func FuzzRunnerProxyCredentialsRemainMasked(f *testing.F) {
	for _, separator := range []string{"", "@", ",", "|", "@,", "@|", "%40"} {
		f.Add(separator, []byte("credential"))
	}
	f.Fuzz(func(t *testing.T, separator string, input []byte) {
		sum := sha256.Sum256(input)
		part := fmt.Sprintf("%x", sum[:16])
		separator = strings.Map(func(r rune) rune {
			if strings.ContainsRune("@,|", r) {
				return r
			}
			return -1
		}, separator)
		if len(separator) > 16 {
			separator = separator[:16]
		}
		password := part + separator + part
		value := "https://reader:" + password + "@cache.invalid/npm"
		for _, local := range []bool{false, true} {
			masker := secrets.NewMasker()
			env, err := procenv.Build(procenv.Install, procenv.Base{Local: local, Captured: []string{"npm_config_registry=" + value}, Ambient: []string{"npm_config_registry=" + value}, SubmitEnvAllow: []string{"npm_config_registry"}, Masker: masker}, procenv.Runtime{})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(slices.Collect(env.Values()), "npm_config_registry="+value) {
				t.Fatal("proxy setting was changed")
			}
			if strings.Contains(masker.Mask(password), part) {
				t.Fatal("proxy credential survived masking")
			}
		}
	})
}
