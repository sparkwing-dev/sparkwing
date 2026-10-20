package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestChildLimitsChargeTheRoot(t *testing.T) {
	storetest.Open(t).TestOnlyChildLimits(t)
}
