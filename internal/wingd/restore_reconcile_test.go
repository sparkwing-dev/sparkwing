package wingd_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func writeLedgerState(t *testing.T, home string, blob []byte) string {
	t.Helper()
	dir := filepath.Join(home, "wingd")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir wingd dir: %v", err)
	}
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	return path
}

func marshalState(t *testing.T, snap admission.Snapshot) []byte {
	t.Helper()
	blob, err := json.Marshal(map[string]any{"schema": 1, "snapshot": snap})
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	return blob
}

func holderRunIDs(t *testing.T, home string) map[string]bool {
	t.Helper()
	qs, err := client.Query(context.Background(), client.Options{Home: home, Version: "v1"})
	if err != nil {
		t.Fatalf("query daemon: %v", err)
	}
	ids := map[string]bool{}
	for _, h := range qs.Holders {
		ids[h.RunID] = true
	}
	return ids
}

func TestStartup_PreservesRestoredLeasesAboveSmallerBudget(t *testing.T) {
	home := shortHome(t)
	writeLedgerState(t, home, marshalState(t, admission.Snapshot{
		TotalMilliCores:     14000,
		TotalMemoryBytes:    24 << 30,
		HeadroomMilliCores:  14000,
		HeadroomMemoryBytes: 24 << 30,
		LeaseSeq:            2,
		Leases: []admission.LeaseState{
			{Seq: 1, ID: "lease-1", Token: "tok-keep", RequestID: "run-keep", MilliCores: 1000, Members: []string{"run-keep"}},
			{Seq: 2, ID: "lease-2", Token: "tok-leak", RequestID: "run-leak", MilliCores: 12871, Members: []string{"run-leak"}},
		},
	}))
	log := &logCapture{}
	budget, err := wingd.ParseBudget("10")
	if err != nil {
		t.Fatalf("parse budget: %v", err)
	}
	startDaemon(t, wingd.Config{
		Home:        home,
		Version:     "v1",
		Sampler:     newFakeSampler(14, 24<<30),
		Budget:      budget,
		GraceWindow: time.Minute,
		Logf:        log.logf,
	})

	if log.contains("shed lease") {
		t.Errorf("restored lease was shed under the smaller budget:\n%s", log.joined())
	}
	ids := holderRunIDs(t, home)
	if !ids["run-keep"] || !ids["run-leak"] {
		t.Errorf("restored run lost arbitration under the smaller budget; holders: %v", ids)
	}
	waiter := ensure(t, home, "v1")
	positions, _ := acquireAsync(waiter, coreReq("run-new", 1))
	waitForQueue(t, positions)
	qs := waitForWaiter(t, home, "run-new")
	if len(qs.Holders) != 2 {
		t.Fatalf("new work displaced restored arbitration: holders=%+v", qs.Holders)
	}
}

func TestStartup_SoftOvercommittedStateRestoresAndServes(t *testing.T) {
	home := shortHome(t)
	writeLedgerState(t, home, marshalState(t, admission.Snapshot{
		TotalMilliCores:     14000,
		TotalMemoryBytes:    24 << 30,
		HeadroomMilliCores:  14000,
		HeadroomMemoryBytes: 24 << 30,
		LeaseSeq:            2,
		Leases: []admission.LeaseState{
			{Seq: 1, ID: "lease-1", Token: "tok-a", RequestID: "run-a", MilliCores: 11200, SoftCores: true, Members: []string{"run-a"}},
			{Seq: 2, ID: "lease-2", Token: "tok-b", RequestID: "run-b", MilliCores: 11200, SoftCores: true, Members: []string{"run-b"}},
		},
	}))
	log := &logCapture{}
	startDaemon(t, wingd.Config{
		Home:        home,
		Version:     "v1",
		Sampler:     newFakeSampler(14, 24<<30),
		GraceWindow: time.Minute,
		Logf:        log.logf,
	})

	if log.contains("shed lease") {
		t.Errorf("soft-overcommitted leases were shed instead of restored:\n%s", log.joined())
	}
	ids := holderRunIDs(t, home)
	if !ids["run-a"] || !ids["run-b"] {
		t.Errorf("restored soft leases missing; holders: %v", ids)
	}
}

func TestStartup_QuarantinesUnrestorableState(t *testing.T) {
	duplicateLease := marshalState(t, admission.Snapshot{
		TotalMilliCores:  8000,
		TotalMemoryBytes: 8 << 30,
		LeaseSeq:         2,
		Leases: []admission.LeaseState{
			{Seq: 1, ID: "lease-1", Token: "tok-a", RequestID: "run-a", MilliCores: 1000, Members: []string{"run-a"}},
			{Seq: 2, ID: "lease-1", Token: "tok-b", RequestID: "run-b", MilliCores: 1000, Members: []string{"run-b"}},
		},
	})
	cases := []struct {
		name string
		blob []byte
	}{
		{"invalid snapshot", duplicateLease},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := shortHome(t)
			path := writeLedgerState(t, home, tc.blob)
			log := &logCapture{}
			startDaemon(t, wingd.Config{
				Home:    home,
				Sampler: newFakeSampler(8, 8<<30),
				Logf:    log.logf,
			})

			if blob, err := os.ReadFile(path); err == nil {
				var fresh struct {
					Schema   int                `json:"schema"`
					Snapshot admission.Snapshot `json:"snapshot"`
				}
				if jerr := json.Unmarshal(blob, &fresh); jerr != nil || fresh.Schema != 1 || len(fresh.Snapshot.Leases) != 0 {
					t.Errorf("re-persisted state.json is not a clean snapshot (schema %d, %d leases, err %v)",
						fresh.Schema, len(fresh.Snapshot.Leases), jerr)
				}
			}
			matches, err := filepath.Glob(path + ".corrupt-*")
			if err != nil || len(matches) != 1 {
				t.Errorf("want exactly one quarantine file, got %v (err %v)", matches, err)
			}
			if !log.contains("quarantined") {
				t.Errorf("expected a quarantine log line, got:\n%s", log.joined())
			}
		})
	}
}

func TestStartupRefusesUnreadableLeaseAuthority(t *testing.T) {
	cases := []struct {
		name string
		blob []byte
	}{
		{"malformed json", []byte("{not json")},
		{"unknown schema", []byte(`{"schema":99,"snapshot":{}}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := shortHome(t)
			path := writeLedgerState(t, home, tc.blob)
			d, err := wingd.New(wingd.Config{Home: home, Sampler: newFakeSampler(8, 8<<30)})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := d.Run(ctx); err == nil {
				t.Fatal("daemon served after losing durable lease authority")
			} else if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "sparkwing daemon recover-state --yes") {
				t.Fatalf("startup error is not actionable: %v", err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("unreadable authority was not preserved: %v", err)
			}
			matches, err := filepath.Glob(path + ".corrupt-*")
			if err != nil || len(matches) != 0 {
				t.Fatalf("unreadable authority was quarantined: %v (err %v)", matches, err)
			}
		})
	}
}

func TestStartupRefusesUnrestorableGuardedAuthority(t *testing.T) {
	snapshot := admission.Snapshot{
		TotalMilliCores:  8000,
		TotalMemoryBytes: 8 << 30,
		LeaseSeq:         2,
		Leases: []admission.LeaseState{
			{Seq: 1, ID: "lease-1", Token: "tok-a", RequestID: "run-a", MilliCores: 1000, Members: []string{"run-a"}},
			{Seq: 2, ID: "lease-1", Token: "tok-b", RequestID: "run-b", MilliCores: 1000, Members: []string{"run-b"}},
		},
	}
	blob, err := json.Marshal(map[string]any{
		"schema":   2,
		"snapshot": snapshot,
		"guards": []map[string]any{{
			"lease_id": "lease-1",
			"run_id":   "run-a",
			"session": map[string]any{
				"leader_pid": 73, "session_id": 73, "birth_token": "birth-73",
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	home := shortHome(t)
	path := writeLedgerState(t, home, blob)
	d, err := wingd.New(wingd.Config{Home: home, Sampler: newFakeSampler(8, 8<<30)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Run(ctx); err == nil {
		t.Fatal("daemon served after discarding unrestorable guarded authority")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("guarded authority was not preserved: %v", err)
	}
	matches, err := filepath.Glob(path + ".corrupt-*")
	if err != nil || len(matches) != 0 {
		t.Fatalf("guarded authority was quarantined: %v (err %v)", matches, err)
	}
}
