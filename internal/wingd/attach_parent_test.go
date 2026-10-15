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
		lease.WatchControl(func(ev wingwire.Evicted) { got <- "evicted " + ev.Key }, func(c wingwire.Cancel) { got <- "cancelled " + c.Reason })
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

// safety: the winner's grant, on the group the root released, is what orders the root's release before the
// child's return; the child was away when its ancestor ended, so only a durable record can reach it.
func TestAChildAwayWhenItsAncestorEndsIsCancelledWhenItReturns(t *testing.T) {
	home := shortHome(t)
	cfg := wingd.Config{Home: home, GraceWindow: 5 * time.Second}
	d := startDaemon(t, cfg)
	group := wingwire.SemaphoreClaim{Name: "__box__:deploy", Capacity: 1, Cost: 1, Policy: wingwire.PolicyQueue}
	root := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{RunID: "p", SemaphoresOnly: true, Semaphores: []wingwire.SemaphoreClaim{group}})
	mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
		RunID: wingwire.NodeHostParticipant("p", "call"), OwnerRunID: "p", OwnerLeaseToken: root.Token,
		SubLease: true, Resources: wingwire.HostResources{Cores: 1},
	})
	child := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{RunID: "c", ParentRunID: "p", ParentNodeID: "call"})
	d.stopAndWait(t)
	startDaemon(t, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	back, err := ensure(t, home, "").Reattach(ctx, root.Token, "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := back.Release(); err != nil {
		t.Fatal(err)
	}
	winner, err := ensure(t, home, "").Acquire(ctx, wingwire.AdmissionRequest{RunID: "winner", SemaphoresOnly: true, Semaphores: []wingwire.SemaphoreClaim{group}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = winner.Release() }()

	returned, err := ensure(t, home, "").Reattach(ctx, child.Token, "c")
	if err != nil {
		return
	}
	defer func() { _ = returned.Release() }()
	select {
	case got := <-watchCancel(returned):
		if !strings.HasPrefix(got, "cancelled ") {
			t.Fatalf("the returning child got %q, want a cancel", got)
		}
	case <-ctx.Done():
		t.Fatal("the child reattached after its ancestor ended and was never cancelled")
	}
}

func TestAnOrphanedRunCannotAttachANewChild(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	group := wingwire.SemaphoreClaim{Name: "__box__:deploy", Capacity: 1, Cost: 1, Policy: wingwire.PolicyQueue}
	root := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{RunID: "root", SemaphoresOnly: true, Semaphores: []wingwire.SemaphoreClaim{group}})
	mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
		RunID: wingwire.NodeHostParticipant("root", "call"), OwnerRunID: "root", OwnerLeaseToken: root.Token,
		SubLease: true, Resources: wingwire.HostResources{Cores: 1},
	})
	mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{RunID: "middle", ParentRunID: "root", ParentNodeID: "call"})
	if err := root.Release(); err != nil {
		t.Fatal(err)
	}
	// safety: the winner's grant orders the root's release, and so the middle's orphan mark, before the leaf asks.
	winner, err := ensure(t, home, "").Acquire(ctx, wingwire.AdmissionRequest{RunID: "winner", SemaphoresOnly: true, Semaphores: []wingwire.SemaphoreClaim{group}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = winner.Release() }()
	leaf, err := ensure(t, home, "").Acquire(ctx, wingwire.AdmissionRequest{RunID: "leaf", ParentRunID: "middle", ParentNodeID: "inner"}, nil)
	if err == nil {
		_ = leaf.Release()
		t.Fatalf("the orphaned middle run attached a new child holding %v, outside the group its ended root held", leaf.Semaphores)
	}
}
