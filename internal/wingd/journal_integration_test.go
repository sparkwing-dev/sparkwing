package wingd_test

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/journal"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

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
	records, _, err := journal.ReadWithStats(dir)
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

func TestHealthProbeProducesNoConnectionJournalRecords(t *testing.T) {
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home})
	if err := probeHome(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	socket, err := wingd.EnsureSocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	incomplete, err := wingd.DialSocket(context.Background(), socket, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = incomplete.Close()
	td.stopAndWait(t)
	dir, err := wingd.StateDir(home)
	if err != nil {
		t.Fatal(err)
	}
	records, _, err := journal.ReadWithStats(dir)
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
	disconnected := make(chan struct{}, 1)
	td := startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 8<<30), Logf: func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		if strings.Contains(line, "disconnected while") && strings.HasSuffix(line, "(run node-slot)") {
			disconnected <- struct{}{}
		}
	}})
	parentClient := ensure(t, home, "")
	parent := mustAcquire(t, parentClient, coreReq("parent-run", 1))
	nodeClient := ensure(t, home, "")
	req := coreReq("node-slot", 0.2)
	req.OwnerRunID = "parent-run"
	req.OwnerLeaseToken = parent.Token
	req.Pipeline = "test-pipeline"
	req.SubLease = true
	mustAcquire(t, nodeClient, req)
	if err := nodeClient.Close(); err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	select {
	case <-disconnected:
	case <-wait.Done():
		t.Fatal("node disconnect was not observed")
	}
	if err := parent.Release(); err != nil {
		t.Fatal(err)
	}
	_ = parentClient.Close()
	td.stopAndWait(t)
	dir, err := wingd.StateDir(home)
	if err != nil {
		t.Fatal(err)
	}
	records, _, err := journal.ReadWithStats(dir)
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
