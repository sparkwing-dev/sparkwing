package runners

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestRunnerOperatorEnvironmentNeverInheritsAuthorityProperty(t *testing.T) {
	for seed := 0; seed < 32; seed++ {
		name := fmt.Sprintf("CUSTOM_%d_TOKEN", seed)
		value := fmt.Sprintf("operator-note-%064d", seed)
		masker := secrets.NewMasker()
		base := procenv.Base{Ambient: []string{"AWS_PROFILE=ci", "AWS_REGION=us-east-1", "DOCKER_HOST=unix:///var/run/docker.sock", "BUILD_NOTE=" + value, "AWS_SECRET_ACCESS_KEY=unlisted", name + "=private", "SPARKWING_AGENT_TOKEN=swr_runner", "SPARKWING_NODE_CLAIM_HOLDER=forged", "GITHUB_PAT=ghp_live"}, SubmitEnvAllow: []string{"AWS_PROFILE", "AWS_REGION", "DOCKER_*", "BUILD_NOTE", name, "SPARKWING_*", "GITHUB_PAT"}, Masker: masker}
		env, err := procenv.Build(procenv.Install, base, procenv.Runtime{})
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range base.Ambient[:4] {
			if !slices.Contains(slices.Collect(env.Values()), entry) {
				t.Fatalf("lost allowed operator setting %q", entry)
			}
		}
		for _, entry := range base.Ambient[4:] {
			if slices.Contains(slices.Collect(env.Values()), entry) {
				t.Fatalf("inherited authority through %q", strings.SplitN(entry, "=", 2)[0])
			}
		}
		if strings.Contains(masker.Mask(value), value) || masker.Mask("us-east-1") != "us-east-1" {
			t.Fatal("operator mask classification changed")
		}
	}
}
