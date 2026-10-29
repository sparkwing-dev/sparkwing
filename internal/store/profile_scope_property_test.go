package store

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"
)

func TestProfileTenantIsolationProperty(t *testing.T) {
	s := authorityStore(t)
	a, b := &Tenant{s: s, team: "a"}, &Tenant{s: s, team: "b"}
	random := rand.New(rand.NewPCG(42, 317))
	for range 96 {
		pipeline := fmt.Sprintf("%s/%x", []string{"shared", "' OR 1=1 --", "_%", "a/b", "\u96ea"}[random.IntN(5)], random.Uint64())
		node := []string{"", "job", "' OR 1=1 --", "_%", "\u96ea"}[random.IntN(5)]
		for j, tenant := range []*Tenant{a, b} {
			observation := ProfileObservation{Duration: time.Duration(j+1) * time.Second, PeakCores: float64(j + 1), PeakMemoryBytes: int64(j+1) << 20, CPUMeasured: true}
			if err := tenant.RecordProfileObservation(t.Context(), pipeline, node, observation); err != nil {
				t.Fatal(err)
			}
		}
		before, err := b.GetPipelineProfile(t.Context(), pipeline, node)
		if err != nil || before == nil {
			t.Fatalf("foreign profile=%+v, %v", before, err)
		}
		samples, err := b.ProfileSamples(t.Context(), pipeline, node)
		if err != nil || len(samples) != 1 || samples[0].PeakCores != 2 {
			t.Fatalf("foreign samples=%+v, %v", samples, err)
		}
		own, err := a.ProfileSamples(t.Context(), pipeline, node)
		if err != nil || len(own) != 1 || own[0].PeakCores != 1 {
			t.Fatalf("read crossed tenant: %+v, %v", own, err)
		}
	}
}
