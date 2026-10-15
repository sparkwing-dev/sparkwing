package orchestrator

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestPodEnvCarriesProxyAndCASettingsButNoRunnerCredential(t *testing.T) {
	writeMachineSettings(t, "run:\n  submit_env_allow: []\n")
	t.Setenv("HTTPS_PROXY", "http://user:proxy-pass@proxy.corp:3128")
	t.Setenv("no_proxy", "localhost")
	t.Setenv("NODE_EXTRA_CA_CERTS", "/etc/corp-ca.pem")
	t.Setenv("SSL_CERT_FILE", "/etc/corp-ca.pem")
	t.Setenv("GITHUB_TOKEN", "runner-github")
	t.Setenv("NODE_OPTIONS", "--require=/tmp/x.js")
	t.Setenv("npm_config_cache", "/runner/npm")
	t.Setenv("GOPROXY", "https://registry.example/go")
	t.Setenv("npm_config_registry", "https://registry.example/npm")
	t.Setenv("PIP_INDEX_URL", "https://registry.example/pypi")
	t.Setenv("PIP_TRUSTED_HOST", "registry.example")
	masker := secrets.NewMasker()
	values, err := podProcessEnv(procenv.Plan, os.Environ(), masker)
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Join(values, "\n")
	for _, want := range []string{
		"HTTPS_PROXY=http://user:proxy-pass@proxy.corp:3128", "no_proxy=localhost",
		"NODE_EXTRA_CA_CERTS=/etc/corp-ca.pem", "SSL_CERT_FILE=/etc/corp-ca.pem",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("pod env lacks %s", want)
		}
	}
	for _, leaked := range []string{"GITHUB_TOKEN=", "NODE_OPTIONS=", "npm_config_cache=", "GOPROXY=", "npm_config_registry=", "PIP_INDEX_URL=", "PIP_TRUSTED_HOST="} {
		if strings.Contains(env, leaked) {
			t.Errorf("pod env carries %s", leaked)
		}
	}
	if got := masker.Mask("via user:proxy-pass@proxy.corp"); strings.Contains(got, "proxy-pass") {
		t.Fatalf("the proxy password was not masked: %s", got)
	}
	install, err := podProcessEnv(procenv.Install, os.Environ(), masker)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range install {
		if !slices.Contains(values, value) {
			t.Fatalf("install environment %q differs from planning", value)
		}
	}
}

func TestRegistrySettingsRequireTheOperatorAllowList(t *testing.T) {
	writeMachineSettings(t, "run:\n  submit_env_allow: [GOPROXY, npm_config_registry, PIP_INDEX_URL, PIP_TRUSTED_HOST]\n")
	values := []string{
		"GOPROXY=https://registry.example/go",
		"npm_config_registry=https://registry.example/npm",
		"PIP_INDEX_URL=https://registry.example/pypi",
		"PIP_TRUSTED_HOST=registry.example",
	}
	got, err := podProcessEnv(procenv.Install, values, secrets.NewMasker())
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if !slices.Contains(got, value) {
			t.Fatalf("allowed registry setting %q missing from %v", value, got)
		}
	}
}

func TestRunnerInstallOutputMasksTheProxyCredentials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the hook prints with sh")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm is not on PATH")
	}
	t.Setenv("HTTPS_PROXY", "http://user:proxy-pass@proxy.corp:3128")
	dir := t.TempDir()
	for name, body := range map[string]string{
		"package.json": `{"name":"hooked","version":"1.0.0","main":"pipeline.ts",` +
			`"scripts":{"postinstall":"echo proxy=$HTTPS_PROXY"}}`,
		"package-lock.json": `{"name":"hooked","version":"1.0.0","lockfileVersion":3,"requires":true,` +
			`"packages":{"":{"name":"hooked","version":"1.0.0","hasInstallScript":true}}}`,
		"pipeline.ts": "",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	// safety: nothing installs the SDK, so the entry stays unresolved; only the install's output matters here.
	_, _ = prepareTypeScriptOnRunner(t.Context(), dir, &out)
	if !strings.Contains(out.String(), "proxy=") {
		t.Fatalf("the hook did not run:\n%s", out.String())
	}
	if strings.Contains(out.String(), "proxy-pass") {
		t.Fatalf("install output carried the proxy password:\n%s", out.String())
	}
}

func TestInstallOutputMasksACredentialSplitAtTheLineLimit(t *testing.T) {
	const limit = 1 << 20
	secret := "proxy-pass-0123456789"
	masker := secrets.NewMasker()
	masker.Register(secret)
	var dst bytes.Buffer
	out, err := maskedInstallOutput(t.Context(), masker, &dst)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("a", limit-5) + secret + "\n" + "next " + secret + "\n"
	// safety: the first write ends at the line limit, inside the credential, and the second ends three bytes later.
	for _, chunk := range []string{line[:limit], line[limit : limit+3], line[limit+3:]} {
		if _, err := io.WriteString(out, chunk); err != nil {
			t.Fatal(err)
		}
	}
	out.Flush()
	// safety: each piece is printed on its own line, so the check joins them back before looking for the value.
	if joined := strings.ReplaceAll(dst.String(), "\n", ""); strings.Contains(joined, secret) || strings.Contains(joined, "proxy-pass") {
		t.Fatal("a credential split at the line limit reached the output unmasked")
	}
	if !strings.Contains(dst.String(), "next ") {
		t.Fatal("the line after the long one was lost")
	}
}

func TestRunnerInstallGetsTheOperatorAllowListMasked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the hook prints with sh")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm is not on PATH")
	}
	writeMachineSettings(t, "run:\n  submit_env_allow: [REGISTRY_NOTE, AWS_REGION]\n")
	t.Setenv("REGISTRY_NOTE", "long-operator-value-0123")
	t.Setenv("AWS_REGION", "us-west-2")
	t.Setenv("GITHUB_TOKEN", "runner-github")
	dir := t.TempDir()
	for name, body := range map[string]string{
		"package.json": `{"name":"hooked","version":"1.0.0","main":"pipeline.ts",` +
			`"scripts":{"postinstall":"echo note=$REGISTRY_NOTE region=$AWS_REGION gh=$GITHUB_TOKEN"}}`,
		"package-lock.json": `{"name":"hooked","version":"1.0.0","lockfileVersion":3,"requires":true,` +
			`"packages":{"":{"name":"hooked","version":"1.0.0","hasInstallScript":true}}}`,
		"pipeline.ts": "",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	_, _ = prepareTypeScriptOnRunner(t.Context(), dir, &out)
	got := out.String()
	switch {
	case !strings.Contains(got, "note=*** ") || !strings.Contains(got, "region=us-west-2"):
		t.Fatalf("the install lost an allow-listed name:\n%s", got)
	case strings.Contains(got, "long-operator-value-0123"):
		t.Fatalf("a long allow-listed value reached the install output unmasked:\n%s", got)
	case strings.Contains(got, "runner-github"):
		t.Fatalf("an unlisted runner credential reached the install:\n%s", got)
	}
}
