package apierr_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestErrorEnvelope(t *testing.T) {
	encoded, err := json.Marshal(apierr.Envelope{Error: &apierr.Error{
		Code: apierr.Forbidden, Message: "digest is not readable",
		Details: map[string]any{"reason": apierr.ReasonDigestNotReadable},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	if len(value) != 1 || value["error"] == nil {
		t.Fatalf("invalid outer envelope: %s", encoded)
	}
	var failure map[string]json.RawMessage
	if err := json.Unmarshal(value["error"], &failure); err != nil {
		t.Fatal(err)
	}
	if len(failure) != 3 || failure["code"] == nil || failure["message"] == nil || failure["details"] == nil {
		t.Fatalf("invalid error object: %s", encoded)
	}
}

func TestCodeRegistry(t *testing.T) {
	seen := map[apierr.Code]bool{}
	for _, definition := range apierr.Codes {
		if definition.Code == "" || seen[definition.Code] || definition.Purpose == "" {
			t.Errorf("invalid or duplicate definition: %+v", definition)
		}
		seen[definition.Code] = true
		if definition.HTTPStatus < 400 || definition.HTTPStatus > 599 {
			t.Errorf("non-error status: %+v", definition)
		}
	}
	for code, want := range map[apierr.Code]int{
		apierr.Unauthenticated:   http.StatusUnauthorized,
		apierr.Forbidden:         http.StatusForbidden,
		apierr.LockHeld:          http.StatusConflict,
		apierr.IllegalTransition: http.StatusConflict,
		apierr.PolicyMissing:     http.StatusInternalServerError,
	} {
		if got := apierr.Status(code); got != want {
			t.Errorf("%s status = %d, want %d", code, got, want)
		}
	}
}
