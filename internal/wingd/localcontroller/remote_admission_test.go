package localcontroller

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func FuzzRemoteAdmissionNeverReadsOrCancelsLocalRuns(f *testing.F) {
	f.Add("https://cloud.example", "same-id", false)
	f.Add("http://another.example:8080", "missing", true)
	f.Fuzz(func(t *testing.T, controller, remote string, job bool) {
		if controller == "" || remote == "" {
			return
		}
		var binding Binding
		owner := openFixture(t, func(_ context.Context, b Binding) (Services, error) {
			binding = b
			seedOwner(t, b, "same-id", false)
			return serviceFixture(b), nil
		})
		if _, err := owner.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
		id := wingwire.RemoteRunID(controller, remote)
		if !id.Remote() {
			return
		}
		if terminal, err := owner.IsRunTerminal(id); err != nil || terminal {
			t.Fatalf("remote admission consulted the local store: terminal=%t error=%v", terminal, err)
		}
		holder := string(id)
		if job {
			holder = string(wingwire.NodeHostParticipant(holder, "job"))
		}
		if err := owner.FinalizeCancelledRuns([]string{holder}, "machine admission disconnected"); err != nil {
			t.Fatalf("remote admission tried to cancel a local run: %v", err)
		}
		row, err := binding.Tenant.ReadRun(t.Context(), "same-id")
		if err != nil || row.CancelRequestedAt != nil {
			t.Fatalf("remote admission changed a local run: %+v error=%v", row, err)
		}
		var records int
		if err := binding.Store.DB().QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&records); err != nil || records != 1 {
			t.Fatalf("remote admission created local ownership: records=%d error=%v", records, err)
		}
	})
}
