package sparkwingtest

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets/masktest"
)

func TestTestHostMaskingConformance(t *testing.T) {
	vectors, err := masktest.Load("../../internal/secrets/testdata/masking.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			host := newHost(Options{}).(*host)
			defer host.Close()
			host.masker.Merge(v.Masker())
			attrs, _ := json.Marshal(map[string]any{"nested": []string{v.Input}})
			host.emit(t.Context(), nodeproto.FrameLog{Step: v.Input, Message: v.Input, Attrs: attrs}, "job")
			record := host.records[0]
			expected := host.masker.Log(nodeproto.FrameLog{Step: v.Input, Message: v.Input, Attrs: attrs})
			var expectedAttrs map[string]any
			if err := json.Unmarshal(expected.Attrs, &expectedAttrs); err != nil {
				t.Fatal(err)
			}
			gotAttrs, _ := json.Marshal(record.Attrs)
			wantAttrs, _ := json.Marshal(expectedAttrs)
			if record.Step != expected.Step || record.Msg != expected.Message || string(gotAttrs) != string(wantAttrs) {
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
