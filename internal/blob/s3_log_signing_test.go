package blob_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

func TestS3LogSigningBindsCreateOnlyHeader(t *testing.T) {
	backend := logS3(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("presigning contacted the object store")
		return nil, errors.New("unexpected request")
	})
	for _, name := range []string{"seg-000001.ndjson.gz", "manifest.json"} {
		t.Run(name, func(t *testing.T) {
			key := "logs/r90/teams/acme/runs/run-1/units/plan/g1/" + name
			expiry := time.Now().Add(limits.LogUploadURLTTL)
			raw, headers, err := backend.SignLogPut(t.Context(), key, expiry)
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			signed := strings.Split(u.Query().Get("X-Amz-SignedHeaders"), ";")
			seconds, err := strconv.Atoi(u.Query().Get("X-Amz-Expires"))
			if err != nil || seconds < 1 || seconds > 900 || u.Path != "/bucket/"+key || headers["If-None-Match"] != "*" || !slices.Contains(signed, "if-none-match") {
				t.Fatalf("unfenced log URL: path = %q, query = %v, headers = %v", u.Path, u.Query(), headers)
			}
			for key := range u.Query() {
				if strings.HasPrefix(strings.ToLower(key), "x-amz-checksum-") {
					t.Fatalf("future upload was signed with an empty-body checksum: %s", key)
				}
			}
			for key := range headers {
				if strings.HasPrefix(strings.ToLower(key), "x-amz-checksum-") || strings.EqualFold(key, "Host") || strings.EqualFold(key, "Content-Length") {
					t.Fatalf("future upload carries a fixed body header: %s", key)
				}
			}
		})
	}
}

func TestS3LogSigningRejectsBeforeSigning(t *testing.T) {
	backend := logS3(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid signing contacted the object store")
		return nil, errors.New("unexpected request")
	})
	for _, key := range []string{"teams/acme/cas/" + strings.Repeat("a", 64), "../escape", "logs/r90/teams/acme/runs/run-1/units/plan/g1/seg-000000.ndjson.gz", "logs/r030/teams/acme/runs/run-1/units/plan/g1/manifest.json"} {
		if _, _, err := backend.SignLogPut(t.Context(), key, time.Now().Add(time.Minute)); err == nil {
			t.Errorf("signed invalid log key %q", key)
		}
	}
	key := "logs/r30/teams/acme/runs/run-1/units/trigger/g1/manifest.json"
	now := time.Now()
	for _, expiry := range []time.Time{now, now.Add(-time.Second), now.Add(limits.LogUploadURLTTL + time.Minute)} {
		if _, _, err := backend.SignLogPut(t.Context(), key, expiry); err == nil {
			t.Errorf("signed invalid expiry %v", expiry)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := backend.SignLogPut(ctx, key, now.Add(time.Minute)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled sign = %v", err)
	}
}
