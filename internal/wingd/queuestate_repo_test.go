package wingd_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestQueueState_RepoAndEventsWindow(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Version: "v1"})

	cl := ensure(t, home, "v1")
	mustAcquire(t, cl, wingwire.AdmissionRequest{
		RunID:     "parent-run",
		Pipeline:  "deploy",
		Repo:      "webapp",
		Resources: wingwire.HostResources{Cores: 1},
	})

	qs, err := client.Query(context.Background(), client.Options{Home: home, Version: "v1"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	var parentRow *wingwire.Holder
	for i := range qs.Holders {
		if qs.Holders[i].RunID == "parent-run" {
			parentRow = &qs.Holders[i]
		}
	}
	if parentRow == nil {
		t.Fatalf("holders missing parent: %+v", qs.Holders)
	}
	if parentRow.Repo != "webapp" || parentRow.Parent != "" {
		t.Errorf("parent row = repo %q parent %q, want webapp and no parent", parentRow.Repo, parentRow.Parent)
	}

	if qs.Events == nil {
		t.Fatal("events window missing from queue state")
	}
	if qs.Events.Runs != 1 {
		t.Errorf("events runs = %d, want 1", qs.Events.Runs)
	}
}
