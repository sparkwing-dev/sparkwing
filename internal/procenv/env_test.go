package procenv

import (
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func buildEntries(k Kind, b Base, rt Runtime) ([]string, error) {
	env, err := Build(k, b, rt)
	return slices.Collect(env.Values()), err
}

func TestEnvGoToolKeepsPolicyAndDoesNotMutateItsSource(t *testing.T) {
	masker := secrets.NewMasker()
	env, err := Build(Install, Base{Masker: masker}, Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	configured, err := env.GoTool("GOWORK=off", "GOFLAGS=-modfile=go.mod", "GOPROXY=https://user:private-password@proxy.example")
	if err != nil {
		t.Fatal(err)
	}
	if env.Lookup("GOWORK") != "" || configured.Lookup("GOWORK") != "off" {
		t.Fatal("Go settings mutated the original environment or were not applied")
	}
	if masker.Mask("private-password") == "private-password" {
		t.Fatal("Go proxy credentials were not registered before launch")
	}
	for _, setting := range []string{"AWS_SECRET_ACCESS_KEY=secret", "CUSTOM_TOKEN=secret", "PATH=/untrusted", "GOFLAGS=bad\x00value"} {
		if _, err := env.GoTool(setting); err == nil {
			t.Fatalf("accepted %q", setting)
		}
	}
	command := Command(configured, "unused")
	command.Env[0] = "CUSTOM_TOKEN=mutated"
	if slices.Contains(slices.Collect(configured.Values()), "CUSTOM_TOKEN=mutated") {
		t.Fatal("command owns the environment's backing storage")
	}
}
