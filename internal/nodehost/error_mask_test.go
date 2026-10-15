package nodehost

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func FuzzHostCanonicalErrorMasking(f *testing.F) {
	for _, seed := range []string{"credential", `quoted-"-credential`, "escaped-\\-credential", "unicode-世界", "nested\ncredential"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		secret := "private-" + strings.ToValidUTF8(input, "�") + "-credential"
		masker := secrets.NewMasker()
		masker.Register(secret)
		failure := &apierr.Error{Code: apierr.Forbidden, Message: secret, Hint: secret, Details: map[string]any{
			"reason": apierr.ReasonBinding, secret: []any{secret, map[string]any{secret: secret}},
		}}
		var rendered *apierr.Error
		err := maskHostError(failure, masker.Mask)
		if !errors.As(err, &rendered) || !errors.Is(err, failure) || rendered.Details["reason"] != string(apierr.ReasonBinding) {
			t.Fatalf("lost canonical refusal or cause: %v", err)
		}
		raw, marshalErr := json.Marshal(rendered)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		var decoded any
		if json.Unmarshal(raw, &decoded) != nil || strings.Contains(fmt.Sprint(decoded), secret) || strings.Contains(err.Error(), secret) {
			t.Fatal("host error exposed a registered secret")
		}
		if failure.Details["reason"] != apierr.ReasonBinding || failure.Message != secret {
			t.Fatal("masking mutated the original failure")
		}
	})
}

func TestHostCanonicalErrorMasksNumericDetails(t *testing.T) {
	failure := &apierr.Error{Code: apierr.Forbidden, Details: map[string]any{"count": json.Number("123456789"), "flag": true}}
	var rendered *apierr.Error
	err := maskHostError(failure, func(value string) string {
		return strings.NewReplacer("123456789", "***", "true", "***").Replace(value)
	})
	if !errors.As(err, &rendered) || rendered.Details["count"] != "***" || rendered.Details["flag"] != "***" {
		t.Fatal("scalar details bypassed masking")
	}
}
