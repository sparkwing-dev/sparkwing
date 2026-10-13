package wingd_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestChildAttachByParentNodeTakesItsCoresAndTheChainsGroups(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})
	group := wingwire.SemaphoreClaim{Name: "__box__:deploy", Capacity: 1, Cost: 1, Policy: wingwire.PolicyQueue}
	sibling := wingwire.SemaphoreClaim{Name: "__box__:sibling", Capacity: 1, Cost: 1, Policy: wingwire.PolicyQueue}
	gp := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
		RunID: "gp", SemaphoresOnly: true, Semaphores: []wingwire.SemaphoreClaim{group},
	})
	mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
		RunID: wingwire.NodeHostParticipant("gp", "call"), OwnerRunID: "gp", OwnerLeaseToken: gp.Token,
		SubLease: true, Resources: wingwire.HostResources{Cores: 1},
	})
	mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
		RunID: wingwire.NodeHostParticipant("gp", "other"), OwnerRunID: "gp", OwnerLeaseToken: gp.Token,
		SubLease: true, Resources: wingwire.HostResources{Cores: 1}, Semaphores: []wingwire.SemaphoreClaim{sibling},
	})

	middle := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{RunID: "middle", ParentRunID: "gp", ParentNodeID: "call"})
	if middle.Resources.Cores != 1 {
		t.Errorf("middle attached with %+v, want the 1 core its parent node holds", middle.Resources)
	}
	if !slices.Contains(middle.Semaphores, group.Name) || slices.Contains(middle.Semaphores, sibling.Name) {
		t.Errorf("middle holds %v, want its grandparent run's group and not a sibling node's", middle.Semaphores)
	}

	// safety: the middle run's node took no lease of its own, as a node of an attached run does not.
	leaf := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{RunID: "leaf", ParentRunID: "middle", ParentNodeID: "inner"})
	if leaf.Resources.Cores != 1 || !slices.Contains(leaf.Semaphores, group.Name) {
		t.Errorf("grandchild attached with %+v holding %v, want the chain's core and group", leaf.Resources, leaf.Semaphores)
	}

	_, err := ensure(t, home, "").Acquire(context.Background(), wingwire.AdmissionRequest{RunID: "orphan", ParentRunID: "gone", ParentNodeID: "call"}, nil)
	if err == nil || !strings.Contains(err.Error(), "holds no admission lease") {
		t.Fatalf("attach to a parent holding nothing: err=%v, want a refusal naming the missing lease", err)
	}
}
