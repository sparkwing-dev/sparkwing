package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestHostDiagnosticsMaskingConformance(t *testing.T) {
	raw, err := os.ReadFile("../secrets/testdata/masking.json")
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
			masker := secrets.NewMasker()
			for _, value := range v.Values {
				masker.Register(value)
			}
			var result *apierr.Error
			source := &apierr.Error{Code: apierr.PlanFailed, Message: v.Input, Hint: v.Input, Details: map[string]any{"nested": []string{v.Input}}}
			if !errors.As(maskHostError(source, masker.Mask), &result) || result.Details["nested"].([]any)[0] != v.Want {
				t.Fatal("error details bypassed shared masker")
			}
			host := nodeExecutor{deps: ExecutorDependencies{Mask: masker.Mask, Log: func(_ context.Context, _ NodeInput, frame nodeproto.FrameLog) error {
				var attrs map[string]any
				if err := json.Unmarshal(frame.Attrs, &attrs); err != nil {
					t.Fatal(err)
				}
				if frame.Step != v.Want || frame.Message != v.Want || attrs["nested"].([]any)[0] != v.Want {
					t.Fatal("diagnostic bypassed shared masker")
				}
				return nil
			}}}
			if err := host.diagnostic(t.Context(), NodeInput{}, "info", "event", v.Input, v.Input, map[string]any{"nested": []any{v.Input}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func FuzzExecutorDiagnosticSinks(f *testing.F) {
	f.Add("list,element")
	f.Add("quoted-\"-秘密")
	f.Fuzz(func(t *testing.T, input string) {
		secret := "private-" + strings.ToValidUTF8(input, "�") + "-value"
		masker := secrets.NewMasker()
		masker.Register(secret)
		host := nodeExecutor{deps: ExecutorDependencies{Mask: masker.Mask, Log: func(_ context.Context, _ NodeInput, frame nodeproto.FrameLog) error {
			if masker.ContainsSecret(frame.Step) || masker.ContainsSecret(frame.Message) || masker.ContainsSecret(string(frame.Attrs)) {
				t.Fatal("executor exposed a registered diagnostic value")
			}
			return nil
		}}}
		if err := host.diagnostic(t.Context(), NodeInput{}, "info", "event", secret, secret, map[string]any{secret: []any{secret}}); err != nil {
			t.Fatal(err)
		}
	})
}
