package client

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestCapacityUsesObservedAvailability(t *testing.T) {
	for _, free := range []float64{0, 0.5, 4} {
		capacity, available, err := Capacity(wingwire.QueueState{Resources: []wingwire.ResourceState{{Key: "cores", Capacity: 8, Held: 3, Available: free}, {Key: "memory", Capacity: 1024, Available: 512}}})
		if err != nil || capacity.Cores != 8 || available.Cores != free || available.MemoryBytes != 512 {
			t.Fatalf("capacity=%+v available=%+v err=%v", capacity, available, err)
		}
	}
	if _, _, err := Capacity(wingwire.QueueState{}); err == nil {
		t.Fatal("missing observation invented capacity")
	}
}
