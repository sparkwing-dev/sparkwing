package orchestrator

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
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

func TestDispatchesLocally_FollowsTheDispatchBackend(t *testing.T) {
	if !LocalBackends(Paths{Root: t.TempDir()}, nil, nil).dispatchesLocally() {
		t.Error("a run on this machine's store is not reported local")
	}
	if RemoteBackends(client.NewWithToken("http://127.0.0.1:1", nil, ""), nil, nil, nil, 0).dispatchesLocally() {
		t.Error("a run a controller dispatched is reported local")
	}
}
