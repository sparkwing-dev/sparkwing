package secrets

import (
	"encoding/json"
	"os"
	"testing"
)

func TestMaskingConformanceVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/masking.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name                       string
		Values, Lists, MergeValues []string
		Input, Want                string
		JSONInput, JSONWant        string
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			m := newTestMasker(v.MergeValues...)
			merged := NewMasker()
			merged.Merge(m)
			m = merged
			for _, value := range v.Values {
				m.Register(value)
			}
			for _, value := range v.Lists {
				m.RegisterList(value)
			}
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
	m := NewMasker()
	m.RegisterList("first-private, second-private")
	for _, value := range []string{"first-private", "second-private"} {
		if m.Mask(value) != "***" || !m.ContainsSecret("job-"+value) {
			t.Fatal("list element is not registered")
		}
	}
}
