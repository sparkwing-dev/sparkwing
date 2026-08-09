package orchestrator

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestReportQueuedUsesCanonicalNodeID(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	la := &LocalAdmission{Out: io.Discard}
	backends := LocalBackends(PathsAt(t.TempDir()), st, nil)

	for _, tc := range []struct {
		name, participant, requestID, want string
	}{
		{name: "root", requestID: "run-1", want: ""},
		{name: "plan", requestID: "run-1/plan", want: ""},
		{name: "node", participant: "compile", requestID: "run-1/node-host/Y29tcGlsZQ", want: "compile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runID := "run-" + tc.name
			if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "test", Status: "running", StartedAt: time.Now()}); err != nil {
				t.Fatalf("CreateRun: %v", err)
			}
			la.reportQueued(ctx, backends, runID, tc.participant, tc.requestID, tc.requestID, wingwire.Queued{Position: 1, QueueLength: 2})
			events, err := st.ListEventsAfter(ctx, runID, 0, 10)
			if err != nil {
				t.Fatalf("ListEventsAfter: %v", err)
			}
			if len(events) != 1 || events[0].NodeID != tc.want {
				t.Fatalf("events = %+v, want one event with node_id %q", events, tc.want)
			}
		})
	}
}

func TestLeaseCarriesHost(t *testing.T) {
	tests := []struct {
		name  string
		lease *wingdclient.Lease
		want  bool
	}{
		{name: "nil", lease: nil, want: false},
		{name: "zero", lease: &wingdclient.Lease{}, want: false},
		{name: "cores", lease: &wingdclient.Lease{Resources: wingwire.HostResources{Cores: 0.1}}, want: true},
		{name: "memory", lease: &wingdclient.Lease{Resources: wingwire.HostResources{MemoryBytes: 1}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := leaseCarriesHost(tt.lease); got != tt.want {
				t.Fatalf("leaseCarriesHost() = %v, want %v", got, tt.want)
			}
		})
	}
}
