package wingd

import (
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func BenchmarkRouteGrantBatch(b *testing.B) {
	for _, count := range []int{100, 500} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			for b.Loop() {
				b.StopTimer()
				d, err := New(Config{Home: b.TempDir()})
				if err != nil {
					b.Fatal(err)
				}
				d.ledger, err = admission.New(admission.Config{TotalCores: float64(count), TotalMemoryBytes: 1 << 30})
				if err != nil {
					b.Fatal(err)
				}
				d.appliedCores = float64(count)
				var events []admission.Event
				for i := range count {
					id := fmt.Sprint(i)
					d.byRun[id] = &conn{runID: id, resources: wingwire.HostResources{Cores: 1}}
					_, next, err := d.ledger.Submit(admission.Request{ID: id, Cores: 1})
					if err != nil {
						b.Fatal(err)
					}
					events = append(events, next...)
				}
				b.StartTimer()
				d.routeLocked(events)
			}
		})
	}
}
