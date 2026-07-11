package wingd_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// TestQueueState_RepoChildAttachmentAndEventsWindow drives a parent
// admission carrying a repo identity, attaches a child to its lease, and
// asserts the queue snapshot carries the repo on both rows, renders the
// child as an attached holder under its parent, and summarizes the grant
// in the events window.
func TestQueueState_RepoChildAttachmentAndEventsWindow(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Version: "v1", GraceWindow: -1})

	cl := ensure(t, home, "v1")
	parent := mustAcquire(t, cl, wingwire.AdmissionRequest{
		RunID:     "parent-run",
		Pipeline:  "deploy",
		Repo:      "webapp",
		Resources: wingwire.HostResources{Cores: 1},
	})

	childConn := ensure(t, home, "v1")
	childLease, err := childConn.Acquire(context.Background(), wingwire.AdmissionRequest{
		RunID:            "child-run",
		Pipeline:         "deploy-child",
		Repo:             "webapp",
		ParentLeaseToken: parent.Token,
	}, nil)
	if err != nil {
		t.Fatalf("child attach: %v", err)
	}
	defer func() { _ = childLease.Release() }()

	qs, err := client.Query(context.Background(), client.Options{Home: home, Version: "v1"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	var parentRow, childRow *wingwire.Holder
	for i := range qs.Holders {
		switch qs.Holders[i].RunID {
		case "parent-run":
			parentRow = &qs.Holders[i]
		case "child-run":
			childRow = &qs.Holders[i]
		}
	}
	if parentRow == nil || childRow == nil {
		t.Fatalf("holders missing parent or child: %+v", qs.Holders)
	}
	if parentRow.Repo != "webapp" || parentRow.Parent != "" {
		t.Errorf("parent row = repo %q parent %q, want webapp and no parent", parentRow.Repo, parentRow.Parent)
	}
	if childRow.Parent != "parent-run" {
		t.Errorf("child row parent = %q, want parent-run", childRow.Parent)
	}
	if childRow.Repo != "webapp" || childRow.Pipeline != "deploy-child" {
		t.Errorf("child row lost display metadata: %+v", childRow)
	}
	if childRow.Resources.Cores != 0 || childRow.Resources.MemoryBytes != 0 {
		t.Errorf("attached child must be charged nothing: %+v", childRow.Resources)
	}

	if qs.Events == nil {
		t.Fatal("events window missing from queue state")
	}
	if qs.Events.Runs != 1 {
		t.Errorf("events runs = %d, want 1 (the parent grant; a child attach is not an admission)", qs.Events.Runs)
	}
}
