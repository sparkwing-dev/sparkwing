package sparkwing_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestNewConcurrencyGroup_RejectsAnEmptyName(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewConcurrencyGroup with an empty name did not panic")
		}
	}()
	sparkwing.NewConcurrencyGroup("", sparkwing.ConcurrencyLimit{Capacity: 1})
}
