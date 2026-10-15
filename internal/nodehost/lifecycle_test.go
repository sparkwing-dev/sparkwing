package nodehost

import (
	"reflect"
	"testing"
)

func TestLayerOwnership(t *testing.T) {
	for _, placement := range []struct {
		name string
		side LayerSide
		want []string
	}{
		{"owner", OwnerSide, []string{"debug", "skip_if", "approval", "memo", "admission"}},
		{"executor", ExecutorSide, []string{"artifacts", "caches", "retry", "timeout"}},
		{"local", "", []string{"debug", "skip_if", "approval", "memo", "admission", "artifacts", "caches", "retry", "timeout"}},
	} {
		t.Run(placement.name, func(t *testing.T) {
			var names []string
			for _, layer := range LayerTable() {
				if layer.PerAttempt != (layer.Name == "timeout") {
					t.Fatalf("%s has the wrong retry scope", layer.Name)
				}
				if placement.side == "" || layer.Side == placement.side {
					names = append(names, layer.Name)
				}
			}
			if !reflect.DeepEqual(names, placement.want) {
				t.Fatalf("layers = %v, want %v", names, placement.want)
			}
		})
	}
}
