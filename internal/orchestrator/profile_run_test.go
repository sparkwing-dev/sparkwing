package orchestrator

import (
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"strings"
	"testing"
)

func TestPinnedLocalRefusalDoesNotEchoUncheckedProfile(t *testing.T) {
	const value = "private-profile-canary"
	err := RefusePinnedLocalRun(&pipelines.Pipeline{Name: "probe", Profile: value})
	if err == nil || strings.Contains(err.Error(), value) || !strings.Contains(err.Error(), "--profile") || !strings.Contains(err.Error(), "sparkwing.yaml") {
		t.Fatalf("pin refusal must give a value-free repair: %v", err)
	}
}
