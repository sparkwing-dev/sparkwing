package orchestrator

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

func TestRefusePinnedLocalRun(t *testing.T) {
	if err := RefusePinnedLocalRun(nil); err != nil {
		t.Fatalf("an undeclared pipeline was refused: %v", err)
	}
	if err := RefusePinnedLocalRun(&pipelines.Pipeline{Name: "build"}); err != nil {
		t.Fatalf("an unpinned pipeline was refused: %v", err)
	}
	err := RefusePinnedLocalRun(&pipelines.Pipeline{Name: "release", Profile: "prod"})
	if err == nil || !strings.Contains(err.Error(), "sparkwing pipeline trigger release --profile <profile-from-sparkwing.yaml>") {
		t.Fatalf("a pinned pipeline ran locally or the refusal named no trigger command: %v", err)
	}
}

func TestPinnedLocalRefusalDoesNotEchoUncheckedProfile(t *testing.T) {
	const value = "private-profile-canary"
	err := RefusePinnedLocalRun(&pipelines.Pipeline{Name: "probe", Profile: value})
	if err == nil || strings.Contains(err.Error(), value) || !strings.Contains(err.Error(), "--profile") || !strings.Contains(err.Error(), "sparkwing.yaml") {
		t.Fatalf("pin refusal must give a value-free repair: %v", err)
	}
}
