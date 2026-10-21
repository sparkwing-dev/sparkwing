package chaos

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestCLIObservationsPreserveSyntheticAdmission(t *testing.T) {
	requireProcessGroups(t)
	h, journal := newProcessHarness(t)
	defer journal.Close()
	h.home = t.TempDir()
	h.cfg = CIConfig(1)
	h.buildBinaries()
	if h.sparkBin == "" {
		t.Fatal("CLI observation regression requires the CLI binary")
	}
	defer h.assertHomeQuiet()
	defer h.cleanup()
	defer func() {
		// A failing observation must also stop a controller it unexpectedly spawned.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Stop(ctx, client.Options{Home: h.home}); err != nil && !errors.Is(err, client.ErrNoDaemon) {
			t.Error(err)
		}
	}()
	cl, err := client.EnsureDaemon(t.Context(), h.readOpts())
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	lease, err := cl.Acquire(t.Context(), wingwire.AdmissionRequest{
		RunID: "synthetic", Resources: wingwire.HostResources{Cores: 1},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	for seed := range int64(9) {
		// Fixed seeds exercise the injector's command choices without timing a chaos run.
		h.rng = rand.New(rand.NewSource(seed)) // #nosec G404 -- deterministic test input
		h.hammerCLI()
		state, err := h.readState()
		if err != nil {
			t.Fatal(err)
		}
		if state.DaemonVersion != "v1.0.0" || len(state.Holders) != 1 {
			t.Fatalf("CLI replaced the admission daemon or lost its holder: %+v", state)
		}
		if _, err := os.Stat(filepath.Join(h.home, "state.db")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("CLI opened a controller runs store in the synthetic admission home: %v", err)
		}
	}
	recorded, err := os.Open(journal.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer recorded.Close()
	decoder := json.NewDecoder(recorded)
	for decoder.More() {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Fields["error"] != nil {
			t.Errorf("CLI observation %s failed: %v", event.Detail, event.Fields["error"])
		}
	}
}
