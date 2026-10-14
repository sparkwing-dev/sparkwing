package projectconfig_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func TestLoad_APinMayNameAProfileTheProjectDoesNotDeclare(t *testing.T) {
	path := writeYAML(t, t.TempDir(), projectconfig.Filename,
		"pipelines:\n  - name: release\n    entrypoint: Release\n    profile: prod\n")
	cfg, err := projectconfig.LoadOld(path)
	if err != nil {
		t.Fatalf("a pin naming a config.yaml profile must load: %v", err)
	}
	if cfg.Pipelines[0].Profile != "prod" {
		t.Fatalf("pin = %q, want prod", cfg.Pipelines[0].Profile)
	}
}
