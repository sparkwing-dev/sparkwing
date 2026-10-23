package blob

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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

func TestS3LogSigningBindsExactBytesAndCreateOnly(t *testing.T) {
	backend := logS3(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("presigning contacted the object store")
		return nil, errors.New("unexpected request")
	})
	body := []byte("segment bytes")
	sum := sha256.Sum256(body)
	for _, name := range []string{"seg-000001.ndjson.gz", "manifest.json"} {
		t.Run(name, func(t *testing.T) {
			key := "logs/r90/teams/acme/runs/run-1/units/plan/g1/" + name
			raw, headers, err := backend.SignLogPut(t.Context(), key, int64(len(body)), digest(string(body)), time.Now().Add(limits.LogUploadURLTTL))
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			signed := strings.Split(u.Query().Get("X-Amz-SignedHeaders"), ";")
			seconds, err := strconv.Atoi(u.Query().Get("X-Amz-Expires"))
			if err != nil || seconds < 1 || seconds > 900 || u.Path != "/bucket/"+key || headers["If-None-Match"] != "*" {
				t.Fatalf("unfenced log URL: path = %q, query = %v, headers = %v", u.Path, u.Query(), headers)
			}
			for _, h := range []string{"if-none-match", "content-length", "x-amz-checksum-sha256"} {
				if !slices.Contains(signed, h) {
					t.Fatalf("log URL does not sign %s: %v", h, signed)
				}
			}
			if headers["Content-Length"] != strconv.Itoa(len(body)) || headers["X-Amz-Checksum-Sha256"] != base64.StdEncoding.EncodeToString(sum[:]) {
				t.Fatalf("log URL headers are not bound to the payload: %v", headers)
			}
		})
	}
}

func TestS3LogSigningRejectsBeforeSigning(t *testing.T) {
	backend := logS3(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid signing contacted the object store")
		return nil, errors.New("unexpected request")
	})
	digest := digest("x")
	for _, key := range []string{"teams/acme/cas/" + strings.Repeat("a", 64), "../escape", "logs/r90/teams/acme/runs/run-1/units/plan/g1/seg-000000.ndjson.gz", "logs/r030/teams/acme/runs/run-1/units/plan/g1/manifest.json"} {
		if _, _, err := backend.SignLogPut(t.Context(), key, 1, digest, time.Now().Add(time.Minute)); err == nil {
			t.Errorf("signed invalid log key %q", key)
		}
	}
	key := "logs/r30/teams/acme/runs/run-1/units/trigger/g1/manifest.json"
	segment := "logs/r30/teams/acme/runs/run-1/units/trigger/g1/seg-000001.ndjson.gz"
	now := time.Now()
	for _, expiry := range []time.Time{now, now.Add(-time.Second), now.Add(limits.LogUploadURLTTL + time.Minute)} {
		if _, _, err := backend.SignLogPut(t.Context(), key, 1, digest, expiry); err == nil {
			t.Errorf("signed invalid expiry %v", expiry)
		}
	}
	for _, c := range []struct {
		key    string
		size   int64
		digest Digest
	}{{key, 0, digest}, {key, limits.LogManifestBytes + 1, digest}, {segment, limits.LogSegmentUploadBytes + 1, digest}, {segment, 1, ""}, {segment, 1, Digest(strings.ToUpper(string(digest)))}} {
		if _, _, err := backend.SignLogPut(t.Context(), c.key, c.size, c.digest, now.Add(time.Minute)); err == nil {
			t.Errorf("signed invalid payload binding %+v", c)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := backend.SignLogPut(ctx, key, 1, digest, now.Add(time.Minute)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled sign = %v", err)
	}
}
