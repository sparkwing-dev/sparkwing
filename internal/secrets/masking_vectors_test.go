package secrets_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets/masktest"
)

func TestMaskingConformanceVectors(t *testing.T) {
	vectors, err := masktest.Load("testdata/masking.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			m := v.Masker()
			if got := m.Mask(v.Input); got != v.Want {
				t.Fatalf("Mask = %q, want %q", got, v.Want)
			}
			if v.JSONInput != "" {
				if got := string(m.Attributes([]byte(v.JSONInput))); got != v.JSONWant {
					t.Fatalf("Attributes = %s, want %s", got, v.JSONWant)
				}
			}
			for split := 0; split <= len(v.Input); split++ {
				stream := m.Stream()
				if got := stream.Mask(v.Input[:split]) + stream.Mask(v.Input[split:]) + stream.Flush(); got != v.Want {
					t.Fatalf("split %d = %q, want %q", split, got, v.Want)
				}
			}
		})
	}
}
