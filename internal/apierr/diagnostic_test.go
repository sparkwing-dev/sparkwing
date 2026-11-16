package apierr

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestLastDiagnosticRequiresAFinalRegisteredEnvelope(t *testing.T) {
	const valid = `{"error":{"code":"bad_request","message":"invalid manifest","hint":"restore main"}}`
	for _, test := range []struct {
		name, output string
		present      bool
	}{
		{"final", "build progress\n" + valid + "\n", true},
		{"earlier", valid + "\nnew failure without an envelope", false},
		{"inner only", `{"code":"bad_request","message":"invalid manifest"}`, false},
		{"unknown code", `{"error":{"code":"unregistered","message":"invalid manifest"}}`, false},
		{"duplicate code", `{"error":{"code":"bad_request","code":"conflict","message":"invalid manifest"}}`, false},
		{"malformed", `{"error":`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			failure, err := LastDiagnostic(test.output)
			if (failure != nil) != test.present || (err == nil) != test.present {
				t.Fatalf("diagnostic = %+v, %v; want present=%t", failure, err, test.present)
			}
			if failure != nil && (failure.Code != apierr.BadRequest || failure.Message != "invalid manifest" || failure.Hint != "restore main") {
				t.Fatalf("final diagnostic changed: %+v", failure)
			}
		})
	}
}
