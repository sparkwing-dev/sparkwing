package orchestrator

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

func writeInnerProfiles(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := fssecure.WriteFile(path, []byte(body)); err != nil {
		t.Fatalf("write profiles: %v", err)
	}
	t.Setenv("SPARKWING_CONFIG", path)
}

func TestRefusePinnedLocalRun(t *testing.T) {
	if err := RefusePinnedLocalRun(nil); err != nil {
		t.Fatalf("an undeclared pipeline was refused: %v", err)
	}
	if err := RefusePinnedLocalRun(&pipelines.Pipeline{Name: "build"}); err != nil {
		t.Fatalf("an unpinned pipeline was refused: %v", err)
	}
	err := RefusePinnedLocalRun(&pipelines.Pipeline{Name: "release", Profile: "prod"})
	if err == nil || !strings.Contains(err.Error(), "sparkwing pipeline trigger release --profile prod") {
		t.Fatalf("a pinned pipeline ran locally or the refusal named no trigger command: %v", err)
	}
}

// A leftover key the strict decoder rejects must not hide a pin from a cron,
// detached run or local child trigger.
func TestRefusePinnedCheckoutRun_FailsWhenTheConfigCannotBeRead(t *testing.T) {
	checkoutAt(t, "defaults:\n  profile: prod\npipelines:\n  - name: release\n    entrypoint: Release\n    profile: prod\n")
	err := refusePinnedCheckoutRun(nil)
	if err == nil || !strings.Contains(err.Error(), "profile pin") {
		t.Fatalf("refusePinnedCheckoutRun with an unreadable config = %v, want a refusal", err)
	}
	checkoutAt(t, "pipelines:\n  - name: release\n    entrypoint: Release\n")
	if err := refusePinnedCheckoutRun(nil); err != nil {
		t.Fatalf("a readable config with no pin was refused: %v", err)
	}
}

func TestRefuseProfileHandoff_FailsARunAnOlderCLIPointedAtAProfile(t *testing.T) {
	t.Setenv(ProfileEnv, "")
	if err := refuseProfileHandoff(); err != nil {
		t.Fatalf("no handoff: %v", err)
	}
	t.Setenv(ProfileEnv, "shared")
	err := refuseProfileHandoff()
	if err == nil || !strings.Contains(err.Error(), "sparkwing pipeline trigger <pipeline> --profile shared") {
		t.Fatalf("handoff = %v, want a refusal naming the trigger command", err)
	}
}
