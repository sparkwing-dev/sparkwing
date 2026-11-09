package engine

import (
	"context"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseauth"
)

// safety: metadata readers cannot create cache directories or persist policy.
func newPolicyReader() (*Resolver, error) {
	dir, err := defaultCacheDirectory()
	if err != nil {
		return nil, err
	}
	keys, err := releaseauth.PublicKeys()
	if err != nil {
		return nil, err
	}
	return &Resolver{config: Config{CacheDirectory: dir, HTTPClient: http.DefaultClient, TrustedKeys: keys}}, nil
}

// ReadIndex verifies policy with cached fallback and rollback protection without writing state.
func ReadIndex(ctx context.Context, now time.Time) (VerifiedIndex, error) {
	reader, err := newPolicyReader()
	if err != nil {
		return VerifiedIndex{}, err
	}
	result, err := reader.indexPolicy(ctx, now, false)
	return result.Index, err
}

// CachedEngineVersion selects the pin's engine from cached policy without creating state.
func CachedEngineVersion(ctx context.Context, pin engineid.Pin) (string, error) {
	reader, err := newPolicyReader()
	if err != nil {
		return "", err
	}
	reader.config.LocalOnly = true
	return reader.EngineVersion(ctx, pin)
}
