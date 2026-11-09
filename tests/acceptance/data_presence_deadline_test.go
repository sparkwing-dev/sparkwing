package acceptance_test

import hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestDataReadHTTPPresenceDeadlineFitsResponseWindow(t *testing.T) {
	for _, lifetime := range []time.Duration{time.Second, time.Hour} {
		t.Run(lifetime.String(), func(t *testing.T) {
			f := newDataHTTPFixture()
			start := time.Now()
			caller := start.Add(lifetime)
			var observed time.Time
			read := f.data.ReadBlob
			f.data.ReadBlob = func(ctx context.Context, a blob.Authority, d blob.Digest) (blob.Blob, error) {
				var ok bool
				observed, ok = ctx.Deadline()
				if !ok {
					t.Fatal("presence metadata read has no deadline")
				}
				return read(ctx, a, d)
			}
			body, err := json.Marshal(map[string]any{"digests": []blob.Digest{dataDigest()}, "deadline": caller})
			if err != nil {
				t.Fatal(err)
			}
			w := authorityHTTP(f.handler(), f.boundary, "POST", "/api/v1/data/has-many", string(body))
			if w.Code != 200 || observed.IsZero() {
				t.Fatalf("presence did not read metadata: deadline=%v, HTTP %d %s", observed, w.Code, w.Body)
			}
			if lifetime < 30*time.Second {
				if !observed.Equal(caller) {
					t.Fatalf("short caller deadline changed: got %v, want %v", observed, caller)
				}
			} else if observed.Before(start.Add(30*time.Second)) || observed.After(time.Now().Add(30*time.Second)) {
				t.Fatalf("presence work exceeds the 30-second response window: %v", observed)
			}
		})
	}
}

func TestDataReadHTTPPresenceRequiresDeadlineAfterAuthentication(t *testing.T) {
	f := newDataHTTPFixture()
	h := f.handler()
	body := `{"digests":[]}`
	w := authorityHTTP(h, f.boundary, "POST", "/api/v1/data/has-many", body)
	assertEngineError(t, w, 400, apierr.BadRequest)
	f.boundary.raw = ""
	w = authorityHTTP(h, f.boundary, "POST", "/api/v1/data/has-many", body)
	assertEngineError(t, w, 401, hosterr.Unauthenticated)
}
