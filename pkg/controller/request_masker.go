package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type requestMaskerKey struct{}

func withRequestMasker(ctx context.Context) (context.Context, *secrets.Masker) {
	if m, ok := ctx.Value(requestMaskerKey{}).(*secrets.Masker); ok {
		return ctx, m
	}
	m := secrets.NewMasker()
	return context.WithValue(ctx, requestMaskerKey{}, m), m
}

// safety: work outside a request gets a masker of its own, so a secret it
// opens is never kept for the life of the server.
func requestMasker(ctx context.Context) *secrets.Masker {
	_, m := withRequestMasker(ctx)
	return m
}
