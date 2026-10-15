package sparkwing_test

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestStepErrorRetainsIdentityAndCause(t *testing.T) {
	cause := errors.New("failed")
	err := &sparkwing.StepError{StepID: "compile", Cause: cause}
	if err.Error() != `step "compile": failed` || !errors.Is(err, cause) {
		t.Fatalf("step failure lost attribution or cause: %v", err)
	}
	for _, absent := range []*sparkwing.StepError{nil, {StepID: "compile"}} {
		if absent.Error() != "" || absent.Unwrap() != nil {
			t.Fatalf("nil step failure = %v", absent)
		}
	}
}

func TestConcurrencyGroupStringIncludesEffectivePolicy(t *testing.T) {
	group := sparkwing.NewConcurrencyGroup("build", sparkwing.ConcurrencyLimit{Capacity: 2})
	if got := group.String(); got != "build(capacity 2, scope global, on-limit queue)" {
		t.Fatal(got)
	}
	group = sparkwing.NewConcurrencyGroup("build", sparkwing.ConcurrencyLimit{Capacity: 2, Scope: sparkwing.ScopeBox, OnLimit: sparkwing.CancelOthers})
	if got := group.String(); got != "build(capacity 2, scope box, on-limit cancel_others)" {
		t.Fatal(got)
	}
}
