package apierr

import (
	"strings"
	"testing"
)

func TestPublicErrorUnmarshalAndIsolatedHomeRemedy(t *testing.T) {
	var e Error
	if err := e.UnmarshalJSON([]byte(`{"code":"not_found","message":"missing","hint":"retry","details":{"reason":"no_output"}}`)); err != nil || e.Code != NotFound || e.Message != "missing" || e.Hint != "retry" || e.Details["reason"] != "no_output" {
		t.Fatalf("error=%+v decode=%v", e, err)
	}
	if err := e.UnmarshalJSON([]byte(`{"code":"a","code":"b","message":"bad"}`)); err == nil {
		t.Fatal("duplicate code accepted")
	}
	refusal := IsolatedHomeRefusal()
	if refusal.Code != IsolatedHomeRequired || !strings.Contains(refusal.Message, "HOME=") || !strings.Contains(refusal.Message, "SPARKWING_HOME=") {
		t.Fatalf("refusal=%+v", refusal)
	}
}
