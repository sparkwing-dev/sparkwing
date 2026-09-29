package store_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestPipelineProfile_ConcurrentObservationsAreRetained(t *testing.T) {
	for _, seeded := range []bool{false, true} {
		name := "new profile"
		if seeded {
			name = "pinned profile"
		}
		t.Run(name, func(t *testing.T) {
			assertConcurrentProfileObservations(t, storetest.Open(t), seeded)
		})
	}
}

func TestPostgresPipelineProfile_ConcurrentObservationsAreRetained(t *testing.T) {
	for _, seeded := range []bool{false, true} {
		name := "new profile"
		if seeded {
			name = "pinned profile"
		}
		t.Run(name, func(t *testing.T) {
			assertConcurrentProfileObservations(t, openPGTestStore(t), seeded)
		})
	}
}

func TestSQLitePipelineProfile_IndependentStoresRetainConcurrentObservations(t *testing.T) {
	for _, seeded := range []bool{false, true} {
		name := "new profile"
		if seeded {
			name = "pinned profile"
		}
		t.Run(name, func(t *testing.T) {
			target := storetest.NewSQLite(t)
			writers := make([]*store.Store, 4)
			for i := range writers {
				writers[i] = target.Open(t)
			}
			assertConcurrentProfileObservations(t, writers[0], seeded, writers...)
		})
	}
}

func assertConcurrentProfileObservations(t *testing.T, st *store.Store, seeded bool, writers ...*store.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	const observations = 12
	if len(writers) == 0 {
		writers = []*store.Store{st}
	}
	if seeded {
		if err := st.UpsertProfilePin(ctx, "concurrent-observations", "build", 2, 512<<20); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, observations)
	var ready sync.WaitGroup
	ready.Add(observations)
	for i := range observations {
		writer := writers[i%len(writers)]
		go func() {
			ready.Done()
			<-start
			results <- writer.RecordProfileObservation(ctx, "concurrent-observations", "build", store.ProfileObservation{
				Duration:  time.Duration(i+1) * time.Second,
				PeakCores: 1, SustainedCores: 1, PeakMemoryBytes: 64 << 20,
				CPUMeasured: true,
			})
		}()
	}
	ready.Wait()
	close(start)
	for range observations {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	profile, err := st.GetPipelineProfile(ctx, "concurrent-observations", "build")
	if err != nil || profile == nil {
		t.Fatalf("profile missing: %v", err)
	}
	if profile.SampleCount != observations {
		t.Errorf("retained %d observations after %d successful calls", profile.SampleCount, observations)
	}
	if seeded && (profile.PinnedCores != 2 || profile.PinnedMemoryBytes != 512<<20) {
		t.Errorf("profile update changed pins: cores=%v memory=%d", profile.PinnedCores, profile.PinnedMemoryBytes)
	}
	var raw []byte
	if err := st.DB().QueryRowContext(ctx, `SELECT samples_json FROM pipeline_profiles WHERE pipeline = 'concurrent-observations' AND node_id = 'build'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var window struct {
		Samples []struct {
			Duration int64 `json:"d"`
		} `json:"samples"`
	}
	if err := json.Unmarshal(raw, &window); err != nil {
		t.Fatal(err)
	}
	seen := make(map[int64]int)
	for _, sample := range window.Samples {
		seen[sample.Duration]++
	}
	for i := range observations {
		duration := int64(time.Duration(i+1) * time.Second)
		if seen[duration] != 1 {
			t.Errorf("observation %d occurs %d times, want once", i+1, seen[duration])
		}
	}
}
