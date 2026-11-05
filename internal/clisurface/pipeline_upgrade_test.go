package clisurface

import "testing"

func TestPipelineUpgradeSelectsTheRepositoryPinCommand(t *testing.T) {
	invocation, err := Parse("sparkwing", []string{"pipeline", "upgrade", "--version", "v0.71.0", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Command.Path != "sparkwing pipeline upgrade" || invocation.Value("version") != "v0.71.0" || !invocation.Bool("dry-run") {
		t.Fatalf("upgrade invocation: %+v", invocation)
	}
	if _, err := Parse("sparkwing", []string{"upgrade"}); err == nil {
		t.Fatal("removed top-level upgrade alias remains accepted")
	}
}
