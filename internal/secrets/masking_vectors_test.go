package secrets_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
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
				if got := string(m.MaskJSON([]byte(v.JSONInput))); got != v.JSONWant {
					t.Fatalf("MaskJSON = %s, want %s", got, v.JSONWant)
				}
			}
			for split := 0; split <= len(v.Input); split++ {
				stream := m.Stream()
				if got := stream.Mask(v.Input[:split], false) + stream.Mask(v.Input[split:], true); got != v.Want {
					t.Fatalf("split %d = %q, want %q", split, got, v.Want)
				}
			}
		})
	}
}

func TestRegisterMasksEachSecretListElement(t *testing.T) {
	m := secrets.NewMasker()
	m.RegisterList("first-private, second-private")
	for _, value := range []string{"first-private", "second-private"} {
		if m.Mask(value) != "***" || !m.ContainsSecret("job-"+value) {
			t.Fatal("list element is not registered")
		}
	}
}
