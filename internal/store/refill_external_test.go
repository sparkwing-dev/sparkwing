package store_test

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// collectDueRefills runs a refill pass that charges nothing and returns the refills it authorized.
func collectDueRefills(ctx context.Context, op *store.Operator) ([]store.Refill, error) {
	var due []store.Refill
	err := op.EachDueRefill(ctx, func(r store.Refill) { due = append(due, r) })
	return due, err
}
