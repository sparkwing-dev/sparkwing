package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestChildSourceBundleRegressions(t *testing.T) {
	store.ChildSourceBundleRegressions(t, func(t *testing.T) *store.Store { return storetest.Open(t) })
}
