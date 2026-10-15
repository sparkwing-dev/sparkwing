package orchestrator

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestPodEnvironmentUsesSharedAllowListValidation(t *testing.T) {
	writeMachineSettings(t, "run:\n  submit_env_allow: [\"*\"]\n")
	for _, kind := range []procenv.Kind{procenv.Plan, procenv.Install} {
		if _, err := podProcessEnv(kind, []string{"AWS_PROFILE=ci"}, secrets.NewMasker()); err == nil {
			t.Fatal("bare wildcard accepted")
		}
	}
}

func FuzzPodEnvironmentKeepsCredentialBoundary(f *testing.F) {
	writeMachineSettings(f, "run:\n  submit_env_allow: [CUSTOM_*, HTTPS_PROXY, BUILD_NOTE]\n")
	f.Add("private-password")
	f.Add("encoded%40password")
	f.Fuzz(func(t *testing.T, raw string) {
		secret := fmt.Sprintf("private-%x", []byte(raw))
		proxy := "http://reader:" + secret + "@proxy.invalid"
		for _, kind := range []procenv.Kind{procenv.Plan, procenv.Install} {
			masker := secrets.NewMasker()
			env, err := podProcessEnv(kind, []string{
				"CUSTOM_TOKEN=" + secret, "BUILD_NOTE=" + secret, "HTTPS_PROXY=" + proxy,
				"SPARKWING_RUNNER_NAME=forged", "SPARKWING_AGENT_TOKEN=" + secret,
			}, masker)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(env, "HTTPS_PROXY="+proxy) || !slices.Contains(env, "BUILD_NOTE="+secret) {
				t.Fatal("allowed input lost")
			}
			for _, entry := range env {
				if strings.HasPrefix(entry, "CUSTOM_TOKEN=") || strings.HasPrefix(entry, "SPARKWING_AGENT_TOKEN=") || strings.HasPrefix(entry, "SPARKWING_RUNNER_NAME=") {
					t.Fatal("credential or forged runtime input inherited")
				}
			}
			if strings.Contains(masker.Mask(proxy+" "+secret), secret) {
				t.Fatal("admitted secret is not masked")
			}
		}
	})
}
