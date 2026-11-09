package nodehost

import (
	"encoding/json"
	"errors"
	"testing"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func FuzzEvalFailureNeverExposesValue(f *testing.F) {
	for _, value := range []string{"null", "true", `"key"`, "[]", "{}", "not-json"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		failure := &apierr.Error{Code: apierr.SDKError, Message: "closure failed"}
		for _, kind := range []string{"", "skip_if", "memo_key", "generate", "before_run", "after_run", "verify"} {
			raw, err := evalValue(nodewire.EvalResult{Value: json.RawMessage(value), Error: failure}, kind)
			if len(raw) != 0 || !errors.Is(err, failure) {
				t.Fatalf("%s exposed failed closure value", kind)
			}
		}
	})
}
