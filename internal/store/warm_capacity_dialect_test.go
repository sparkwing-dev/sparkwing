package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestWarmCapacityPostgres(t *testing.T) {
	store.WarmCapacityModesAndDemand(t, storetest.OpenPostgres(t))
}
