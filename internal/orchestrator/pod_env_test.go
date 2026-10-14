package orchestrator

import (
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
