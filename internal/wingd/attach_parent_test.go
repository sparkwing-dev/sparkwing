package wingd_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
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

func watchCancel(lease *client.Lease) <-chan string {
	got := make(chan string, 2)
	go func() {
		_ = lease.WatchOwnership(func(ev wingwire.Evicted) { got <- "evicted " + ev.Key }, func(c wingwire.Cancel) { got <- "cancelled " + c.Reason }, nil)
	}()
	return got
}

func TestAGrandchildIsHandedEveryGroupUpItsChain(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})
	runGroup := wingwire.SemaphoreClaim{Name: "__box__:deploy", Capacity: 1, Cost: 1, Policy: wingwire.PolicyFail}
	nodeGroup := wingwire.SemaphoreClaim{Name: "__box__:db", Capacity: 1, Cost: 1, Policy: wingwire.PolicyFail}
	gp := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{RunID: "gp", Resources: wingwire.HostResources{Cores: 1}, Semaphores: []wingwire.SemaphoreClaim{runGroup}})
	mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
		RunID: wingwire.NodeSemaphoreParticipant("gp", "call"), OwnerRunID: "gp", OwnerLeaseToken: gp.Token,
		SubLease: true, SemaphoresOnly: true, Semaphores: []wingwire.SemaphoreClaim{nodeGroup},
	})
	mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{RunID: "middle", ParentRunID: "gp", ParentNodeID: "call"})
	leaf := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{RunID: "leaf", ParentRunID: "middle", ParentNodeID: "inner"})
	if !slices.Contains(leaf.Semaphores, runGroup.Name) || !slices.Contains(leaf.Semaphores, nodeGroup.Name) {
		t.Fatalf("grandchild holds %v, want its grandparent's run and node groups", leaf.Semaphores)
	}
	for _, g := range []wingwire.SemaphoreClaim{runGroup, nodeGroup} {
		if _, err := ensure(t, home, "").Acquire(context.Background(), wingwire.AdmissionRequest{RunID: "other-" + g.Name, SemaphoresOnly: true, Semaphores: []wingwire.SemaphoreClaim{g}}, nil); err == nil {
			t.Fatalf("an unrelated run took %s while the chain holds it", g.Name)
		}
	}
}

func TestEvictingTheRootEndsTheWholeChain(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})
	group := wingwire.SemaphoreClaim{Name: "__box__:deploy", Capacity: 1, Cost: 1, Policy: wingwire.PolicyCancelOthers}
	gp := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{RunID: "gp", SemaphoresOnly: true, Semaphores: []wingwire.SemaphoreClaim{group}})
	mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
		RunID: wingwire.NodeHostParticipant("gp", "call"), OwnerRunID: "gp", OwnerLeaseToken: gp.Token,
		SubLease: true, Resources: wingwire.HostResources{Cores: 1},
	})
	middle := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{RunID: "middle", ParentRunID: "gp", ParentNodeID: "call"})
	leaf := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{RunID: "leaf", ParentRunID: "middle", ParentNodeID: "inner"})
	middleEnd, leafEnd := watchCancel(middle), watchCancel(leaf)

	mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{RunID: "newer", SemaphoresOnly: true, Semaphores: []wingwire.SemaphoreClaim{group}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for name, ch := range map[string]<-chan string{"middle": middleEnd, "leaf": leafEnd} {
		select {
		case got := <-ch:
			if !strings.HasPrefix(got, "cancelled ") {
				t.Errorf("%s got %q, want a cancel", name, got)
			}
		case <-ctx.Done():
			t.Fatalf("%s kept running after the root holding its group was evicted", name)
		}
	}
}

func TestReleasingTheRootEndsItsChild(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})
	group := wingwire.SemaphoreClaim{Name: "__box__:deploy", Capacity: 1, Cost: 1, Policy: wingwire.PolicyFail}
	gp := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{RunID: "gp", SemaphoresOnly: true, Semaphores: []wingwire.SemaphoreClaim{group}})
	mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
		RunID: wingwire.NodeHostParticipant("gp", "call"), OwnerRunID: "gp", OwnerLeaseToken: gp.Token,
		SubLease: true, Resources: wingwire.HostResources{Cores: 1},
	})
	child := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{RunID: "child", ParentRunID: "gp", ParentNodeID: "call"})
	ended := watchCancel(child)
	if err := gp.Release(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case got := <-ended:
		if !strings.HasPrefix(got, "cancelled ") {
			t.Fatalf("child got %q, want a cancel", got)
		}
	case <-ctx.Done():
		t.Fatal("the child kept running after the run whose group it was handed released it")
	}
}
