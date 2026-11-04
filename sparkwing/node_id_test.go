package sparkwing_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestNewConcurrencyGroup_RejectsUnknownEnumValues(t *testing.T) {
	mustPanic := func(name string, limit sparkwing.ConcurrencyLimit) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("NewConcurrencyGroup(%q, %+v) did not panic", name, limit)
			}
		}()
		sparkwing.NewConcurrencyGroup(name, limit)
	}
	mustPanic("", sparkwing.ConcurrencyLimit{Capacity: 1})
	mustPanic("g", sparkwing.ConcurrencyLimit{Capacity: 1, OnLimit: "qeue"})
	mustPanic("g", sparkwing.ConcurrencyLimit{Capacity: 1, Scope: "globl"})
	sparkwing.NewConcurrencyGroup("g", sparkwing.ConcurrencyLimit{
		Capacity: 1, OnLimit: sparkwing.CancelOthers, Scope: sparkwing.ScopeBox,
	})
}
