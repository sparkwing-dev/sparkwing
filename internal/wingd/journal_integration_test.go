package wingd_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/journal"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestJournalCorruptIncarnationDoesNotStopAdmission(t *testing.T) {
	for _, fixture := range []struct{ name, content string }{{"empty", ""}, {"unparsable", "not-a-number"}} {
		t.Run(fixture.name, func(t *testing.T) {
			home := shortHome(t)
			dir, err := wingd.StateDir(home)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "incarnation"), []byte(fixture.content), 0o600); err != nil {
				t.Fatal(err)
			}
			td := startDaemon(t, wingd.Config{Home: home})
			c := ensure(t, home, "")
			lease := mustAcquire(t, c, coreReq("run", 0.1))
			if err := lease.Release(); err != nil {
				t.Fatal(err)
			}
			_ = c.Close()
			td.stopAndWait(t)
		})
	}
}

func TestJournalRecordsQueueBlockerAndGrant(t *testing.T) {
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 8<<30)})
	holderClient := ensure(t, home, "")
	defer holderClient.Close()
	queuedClient := ensure(t, home, "")
	defer queuedClient.Close()
	holder := mustAcquire(t, holderClient, semReq("holder", "pool", 1, 1, wingwire.PolicyQueue))
	deniedClient := ensure(t, home, "")
	deniedReq := semReq("nonblocking", "pool", 1, 1, wingwire.PolicyQueue)
	deniedReq.NonBlocking = true
	if lease, err := deniedClient.Acquire(context.Background(), deniedReq, nil); err == nil {
		_ = lease.Release()
		t.Fatal("nonblocking request acquired a held semaphore")
	}
	positions, result := acquireAsync(queuedClient, semReq("waiting", "pool", 1, 1, wingwire.PolicyQueue))
	waitForQueue(t, positions)
	if err := holder.Release(); err != nil {
		t.Fatal(err)
	}
	granted := waitResult(t, result, wingdChurnWait)
	if granted.err != nil {
		t.Fatal(granted.err)
	}
	if err := granted.lease.Release(); err != nil {
		t.Fatal(err)
	}
	td.stopAndWait(t)
	dir, err := wingd.StateDir(home)
	if err != nil {
		t.Fatal(err)
	}
	records, err := journal.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	var requested, queued, grantedRecord, denied bool
	for _, record := range records {
		if record.RunID == "nonblocking" && record.Kind == "denied" {
			denied = record.Data["reason"] == "capacity" && record.Data["nonblocking"] == true
		}
		if record.RunID != "waiting" {
			continue
		}
		switch record.Kind {
		case "request":
			requested = record.Data["semaphores"] != nil
		case "queued":
			queued = record.Data["key"] == "pool" && record.Data["blocker"] == "holder"
		case "grant":
			_, grantedRecord = record.Data["wait_ms"]
		}
	}
	if !requested || !queued || !grantedRecord || !denied {
		t.Fatalf("missing admission evidence: request=%t queued=%t grant=%t denied=%t records=%+v", requested, queued, grantedRecord, denied, records)
	}
}

func TestJournalRecordsChildLineageAndCancelScope(t *testing.T) {
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{
		Home: home,
		Runs: &wingd.FuncRunStore{FinalizeCancelled: func([]string, string) error { return nil }},
	})
	root := ensure(t, home, "")
	rootLease := mustAcquire(t, root, coreReq("root", 1))
	foreign := ensure(t, home, "")
	mustAcquire(t, foreign, coreReq("foreign", 1))
	var departedLease *client.Lease
	for _, member := range []struct{ id, parent string }{
		{"parent", "root"}, {"departed", "parent"}, {"grandchild", "departed"},
	} {
		child := ensure(t, home, "")
		lease := mustAcquire(t, child, wingwire.AdmissionRequest{
			RunID: member.id, ParentRunID: member.parent, ParentLeaseToken: rootLease.Token,
		})
		if member.id == "departed" {
			departedLease = lease
		}
	}
	if err := departedLease.Release(); err != nil {
		t.Fatal(err)
	}
	adopted := ensure(t, home, "")
	mustAcquire(t, adopted, wingwire.AdmissionRequest{
		RunID: "adopted", ParentRunID: "departed", ParentLeaseToken: rootLease.Token,
	})
	wrong := ensure(t, home, "")
	if lease, err := wrong.Acquire(context.Background(), wingwire.AdmissionRequest{
		RunID: "wrong", ParentRunID: "foreign", ParentLeaseToken: rootLease.Token,
	}, nil); err == nil {
		_ = lease.Release()
		t.Fatal("cross-lease parent was accepted")
	}
	control := ensure(t, home, "")
	if found, err := control.CancelLease(context.Background(), "parent"); err != nil || !found {
		t.Fatalf("CancelLease = (%v, %v), want found", found, err)
	}
	late := ensure(t, home, "")
	if lease, err := late.Acquire(context.Background(), wingwire.AdmissionRequest{
		RunID: "late", ParentRunID: "departed", ParentLeaseToken: rootLease.Token,
	}, nil); err == nil {
		_ = lease.Release()
		t.Fatal("cancelled ancestor allowed child attach")
	}
	td.stopAndWait(t)
	dir, err := wingd.StateDir(home)
	if err != nil {
		t.Fatal(err)
	}
	records, err := journal.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	var attached, wrongParent, blocked, cancelled bool
	for _, record := range records {
		switch {
		case record.RunID == "adopted" && record.Kind == "child_attach":
			attached = record.Data["requested_parent"] == "departed" && record.Data["resolved_parent"] == "parent"
		case record.RunID == "wrong" && record.Kind == "rejected":
			wrongParent = record.Data["reason"] == "parent resolution failed" && record.Data["requested_parent"] == "foreign" && record.Data["parent_run_id"] == "foreign"
		case record.RunID == "late" && record.Kind == "rejected":
			blocked = record.Data["reason"] == "parent cancelled" && record.Data["parent_run_id"] == "departed" && record.Data["requested_parent"] == "departed" && record.Data["resolved_parent"] == "root"
		case record.RunID == "parent" && record.Kind == "cancel":
			affected, affectedOK := record.Data["affected_runs"].([]any)
			blockedRuns, blockedOK := record.Data["blocked_runs"].([]any)
			if affectedOK && blockedOK {
				cancelled = containsRunIDs(affected, "parent", "grandchild", "adopted") && containsRunIDs(blockedRuns, "parent", "grandchild", "adopted", "departed")
			}
		}
	}
	if !attached || !wrongParent || !blocked || !cancelled {
		t.Fatalf("journal lineage evidence: attached=%t resolution=%t blocked=%t cancel=%t", attached, wrongParent, blocked, cancelled)
	}
}

func containsRunIDs(values []any, ids ...string) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		id, ok := value.(string)
		if ok {
			seen[id] = true
		}
	}
	for _, id := range ids {
		if !seen[id] {
			return false
		}
	}
	return true
}

func TestHealthProbeProducesNoConnectionJournalRecords(t *testing.T) {
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home})
	if err := client.HealthProbe(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	socket, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	incomplete, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	_ = incomplete.Close()
	td.stopAndWait(t)
	dir, err := wingd.StateDir(home)
	if err != nil {
		t.Fatal(err)
	}
	records, err := journal.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if strings.HasPrefix(record.Kind, "connection_") || record.Kind == "message_refused" || record.Kind == "handshake_refused" {
			t.Fatalf("health probe produced %s: %+v", record.Kind, record)
		}
	}
}

func TestJournalConnectionIdentityAndOwnedNodeSlot(t *testing.T) {
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 8<<30)})
	parentClient := ensure(t, home, "")
	parent := mustAcquire(t, parentClient, coreReq("parent-run", 1))
	nodeClient := ensure(t, home, "")
	req := coreReq("node-slot", 0.2)
	req.OwnerRunID = "parent-run"
	req.OwnerLeaseToken = parent.Token
	req.Pipeline = "test-pipeline"
	req.SubLease = true
	node := mustAcquire(t, nodeClient, req)
	if err := node.Release(); err != nil {
		t.Fatal(err)
	}
	_ = nodeClient.Close()
	if err := parent.Release(); err != nil {
		t.Fatal(err)
	}
	_ = parentClient.Close()
	td.stopAndWait(t)
	dir, err := wingd.StateDir(home)
	if err != nil {
		t.Fatal(err)
	}
	records, err := journal.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	var opened, handshake, closed, grant bool
	for _, record := range records {
		if record.Data != nil {
			checkJournalDataKeys(t, record.Data)
		}
		if record.Kind == "grant" && record.RunID == "node-slot" {
			grant = record.Data["owner_run_id"] == "parent-run"
		}
		if record.RunID != "node-slot" {
			continue
		}
		switch record.Kind {
		case "request":
			if _, exists := record.Data["semaphores"]; exists {
				t.Fatal("empty semaphores were serialized")
			}
		case "connection_closed":
			closed = record.Pipeline == "test-pipeline"
			if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
				closed = closed && record.PID == os.Getpid()
			}
		}
	}
	for _, record := range records {
		switch record.Kind {
		case "connection_opened":
			opened = record.PID == os.Getpid()
		case "connection_handshake":
			handshake = record.PID == os.Getpid()
		}
	}
	if !grant || !closed || ((runtime.GOOS == "linux" || runtime.GOOS == "darwin") && (!opened || !handshake)) {
		t.Fatalf("missing connection identity or node grant: opened=%t handshake=%t closed=%t grant=%t", opened, handshake, closed, grant)
	}
}

func checkJournalDataKeys(t *testing.T, value any) {
	t.Helper()
	switch v := value.(type) {
	case nil:
		t.Fatal("journal data contains null")
	case map[string]any:
		for key, child := range v {
			if strings.ToLower(key) != key {
				t.Errorf("journal key %q is not snake_case", key)
			}
			checkJournalDataKeys(t, child)
		}
	case []any:
		for _, child := range v {
			checkJournalDataKeys(t, child)
		}
	}
}
