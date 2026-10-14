package orchestrator

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestPodEnvCarriesProxyAndCASettingsButNoRunnerCredential(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://user:proxy-pass@proxy.corp:3128")
	t.Setenv("no_proxy", "localhost")
	t.Setenv("NODE_EXTRA_CA_CERTS", "/etc/corp-ca.pem")
	t.Setenv("SSL_CERT_FILE", "/etc/corp-ca.pem")
	t.Setenv("GITHUB_TOKEN", "runner-github")
	t.Setenv("NODE_OPTIONS", "--require=/tmp/x.js")
	t.Setenv("npm_config_cache", "/runner/npm")
	masker := secrets.NewMasker()
	env := strings.Join(podSessionEnv(masker), "\n")
	for _, want := range []string{
		"HTTPS_PROXY=http://user:proxy-pass@proxy.corp:3128", "no_proxy=localhost",
		"NODE_EXTRA_CA_CERTS=/etc/corp-ca.pem", "SSL_CERT_FILE=/etc/corp-ca.pem",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("pod env lacks %s", want)
		}
	}
	for _, leaked := range []string{"GITHUB_TOKEN=", "NODE_OPTIONS=", "npm_config_cache="} {
		if strings.Contains(env, leaked) {
			t.Errorf("pod env carries %s", leaked)
		}
	}
	if got := masker.Mask("via user:proxy-pass@proxy.corp"); strings.Contains(got, "proxy-pass") {
		t.Fatalf("the proxy password was not masked: %s", got)
	}
	if got := strings.Join(PodBaseEnv(), "\n"); got != env {
		t.Fatalf("installs and node processes got different environments:\n%s\n--\n%s", got, env)
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
