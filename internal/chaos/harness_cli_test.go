package chaos

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	h.cfg = ciConfig(1)
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
	// Exercise the restart gap too: observation must not create a controller
	// whose production grace and idle periods outlive this harness.
	for seed := range int64(9) {
		h.rng = rand.New(rand.NewSource(seed)) // #nosec G404 -- deterministic test input
		h.hammerCLI()
		if _, err := client.Query(t.Context(), h.readOpts()); !errors.Is(err, client.ErrNoDaemon) {
			t.Fatalf("CLI started a daemon during the restart gap: %v", err)
		}
		if _, err := os.Stat(filepath.Join(h.home, "state.db")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("CLI opened a controller store during the restart gap: %v", err)
		}
	}
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
	recorded, err := os.Open(journal.filename())
	if err != nil {
		t.Fatal(err)
	}
	defer recorded.Close()
	decoder := json.NewDecoder(recorded)
	index := 0
	seen := [2]map[string]bool{{}, {}}
	for {
		var event event
		if err := decoder.Decode(&event); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if event.Kind != "cli" {
			continue
		}
		phase := index / 9
		if phase >= len(seen) {
			t.Fatal("unexpected extra CLI observation")
		}
		seen[phase][event.Detail] = true
		index++
		if index > 9 && event.Fields["error"] != nil {
			t.Errorf("CLI observation %s failed: %v", event.Detail, event.Fields["error"])
		}
	}
	if index != 18 {
		t.Fatalf("recorded %d CLI observations, want 18", index)
	}
	for phase, commands := range seen {
		for _, command := range []string{"daemon events --limit 5", "info"} {
			if !commands[command] {
				t.Errorf("phase %d did not exercise %s", phase, command)
			}
		}
	}
}
