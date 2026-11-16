package engine

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

func TestPolicyLookupKeepsFailureDuringCancellation(t *testing.T) {
	for _, sdk := range []bool{false, true} {
		t.Run(map[bool]string{false: "index", true: "sdk mapping"}[sdk], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			genuine := errors.New("policy read failed")
			reached := false
			resolver, err := NewResolver(Config{CacheDirectory: t.TempDir(), LocalOnly: sdk, HTTPClient: &http.Client{Transport: installedTransport(func(*http.Request) (*http.Response, error) { reached = true; cancel(); return nil, genuine })}})
			if err != nil {
				t.Fatal(err)
			}
			if sdk {
				_, err = resolver.EngineVersion(ctx, engineid.Pin{SDK: engineid.GoSDK, Version: "v0.1.0"})
			} else {
				_, err = resolver.Index(ctx, time.Now())
			}
			if !reached || !errors.Is(err, genuine) {
				t.Fatalf("policy failure replaced during cancellation: reached=%v error=%v", reached, err)
			}
		})
	}
}
