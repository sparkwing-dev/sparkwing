package sparkwingtest

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestTestHostMaskingConformance(t *testing.T) {
	raw, err := os.ReadFile("../../internal/secrets/testdata/masking.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name        string
		Values      []string
		Input, Want string
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			host := newHost(Options{}).(*host)
			defer host.Close()
			for _, value := range v.Values {
				if err := host.Mask(t.Context(), value); err != nil {
					t.Fatal(err)
				}
			}
			attrs, _ := json.Marshal(map[string]any{"nested": []string{v.Input}})
			host.emit(t.Context(), nodeproto.FrameLog{Step: v.Input, Message: v.Input, Attrs: attrs}, "job")
			record := host.records[0]
			if record.Step != v.Want || record.Msg != v.Want || record.Attrs["nested"].([]any)[0] != v.Want {
				t.Fatalf("test host differs from production: %+v", record)
			}
		})
	}
}

func FuzzTestHostDiagnosticSinks(f *testing.F) {
	f.Add("list,element")
	f.Add("quoted-\"-秘密")
	f.Fuzz(func(t *testing.T, input string) {
		secret := "private-" + strings.ToValidUTF8(input, "�") + "-value"
		host := newHost(Options{}).(*host)
		defer host.Close()
		if err := host.Mask(t.Context(), secret); err != nil {
			t.Fatal(err)
		}
		attrs, _ := json.Marshal(map[string]any{secret: []string{secret}})
		host.emit(t.Context(), nodeproto.FrameLog{Step: secret, Message: secret, Attrs: attrs}, "job")
		record := host.records[0]
		if host.ContainsSecret(record.Step) || host.ContainsSecret(record.Msg) {
			t.Fatal("test host exposed secret text")
		}
		raw, _ := json.Marshal(record.Attrs)
		if host.ContainsSecret(string(raw)) {
			t.Fatal("test host exposed a secret attribute")
		}
	})
}
