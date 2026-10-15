package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestValidateRunInvocation_FailsClosedOnMalformedClassification(t *testing.T) {
	run := store.Run{
		Invocation: map[string]any{
			"inputs_hash":                 "sha256:oracle",
			store.InvocationSecretArgsKey: []string{""},
		},
	}
	if err := store.ValidateRunInvocation(run); err != store.ErrSecretInputHash {
		t.Fatalf("ValidateRunInvocation error = %v, want ErrSecretInputHash", err)
	}
}
