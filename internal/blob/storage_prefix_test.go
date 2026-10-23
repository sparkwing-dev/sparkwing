package blob

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func FuzzS3StoragePrefix(f *testing.F) {
	for _, prefix := range []string{"", "controller", "/deployment/nested-root/", "with spaces/日本語/%2e%2e", "../escape", "a/../b", "a//b", "a\\b", "a\x00b"} {
		f.Add(prefix, "teams/acme/pending/ticket")
		f.Add(prefix, "teams/acme/cas/"+string(digest("payload")))
		f.Add(prefix, "logs/r90/teams/acme/runs/run/units/plan/g1/manifest.json")
		f.Add(prefix, "teams/acme/pending/../../escape")
	}
	f.Fuzz(func(t *testing.T, prefix, key string) {
		client := logS3Client(func(*http.Request) (*http.Response, error) {
			t.Fatal("signing performed network I/O")
			return nil, nil
		})
		backend, err := NewS3(client, "bucket", prefix)
		if err != nil {
			return
		}
		root := strings.Trim(prefix, "/")
		if root != "" {
			root += "/"
		}
		check := func(raw string) {
			t.Helper()
			u, err := url.Parse(raw)
			if err != nil || u.Path != "/bucket/"+root+key {
				t.Fatalf("prefix %q, key %q signed as %q: %v", prefix, key, raw, err)
			}
		}
		for _, method := range []string{http.MethodPut, http.MethodGet} {
			signed, err := backend.Sign(t.Context(), capability.ObjectToSign{Method: method, Key: key, Size: 7, SHA256: string(digest("payload")), ExpiresAt: time.Now().Add(time.Minute)})
			if err == nil {
				check(signed.URL)
			}
		}
		if raw, _, err := backend.SignLogPut(t.Context(), key, 7, digest("payload"), time.Now().Add(time.Minute)); err == nil {
			check(raw)
		}
	})
}
