package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestFilesystemDataStartup(t *testing.T) {
	for _, fixture := range []struct{ external, address, host string }{
		{"", "127.0.0.1:4344", "127.0.0.1:4344"},
		{"", "[::1]:4344", "[::1]:4344"},
		{"https://controller.invalid", "0.0.0.0:4344", "controller.invalid"},
		{"https://controller.invalid/prefix", "0.0.0.0:4344", "controller.invalid"},
	} {
		t.Run(fixture.host+fixture.address, func(t *testing.T) {
			root := t.TempDir()
			objects, err := openFilesystemData(root, fixture.external, fixture.address, filesystemTestLogNow)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := objects.Close(); err != nil {
					t.Error(err)
				}
			})
			body := []byte("startup")
			digest := fmt.Sprintf("%x", sha256.Sum256(body))
			key := "teams/tenant/pending/ticket"
			signed, err := objects.Sign(t.Context(), capability.ObjectToSign{Method: http.MethodPut, Key: key, Size: int64(len(body)), SHA256: digest, ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			target, err := url.Parse(signed.URL)
			base, parseErr := url.Parse(fixture.external)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			if err != nil || target.Host != fixture.host || target.Path != base.Path+"/api/v1/data/uploads/ticket" {
				t.Fatalf("signed target = %s: %v", signed.URL, err)
			}
			server := controller.New(nil, nil).WithDataObjects(objects)
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPut, signed.URL, bytes.NewReader(body))
			request.URL.Path = strings.TrimPrefix(request.URL.Path, base.Path)
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusCreated {
				t.Fatalf("PUT = %d: %s", response.Code, response.Body)
			}
			object := blob.Blob{Team: "tenant", Digest: blob.Digest(digest), Size: int64(len(body))}
			if err := objects.CommitObject(t.Context(), key, object); err != nil {
				t.Fatal(err)
			}
			stored, err := os.ReadFile(filepath.Join(root, "data", "teams", "tenant", "cas", digest))
			if err != nil || !bytes.Equal(stored, body) {
				t.Fatalf("published bytes = %q: %v", stored, err)
			}
		})
	}
}

func TestFilesystemDataStartupRefusesInvalidConfiguration(t *testing.T) {
	for _, fixture := range []struct{ external, address string }{{"", "invalid"}, {"ftp://controller.invalid", "127.0.0.1:4344"}, {"https://controller.invalid?query=1", "127.0.0.1:4344"}} {
		objects, err := openFilesystemData(t.TempDir(), fixture.external, fixture.address, filesystemTestLogNow)
		if err == nil {
			if closeErr := objects.Close(); closeErr != nil {
				t.Error(closeErr)
			}
			t.Fatal("invalid configuration opened a backend")
		}
	}
}

func TestS3DataStartupPreservesURLPrefix(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture")
	t.Setenv("AWS_ENDPOINT_URL_S3", "https://objects.invalid")
	for _, prefix := range []string{"", "controller/", "deployment/nested-root/", "space here/%2e%2e/"} {
		t.Run(prefix, func(t *testing.T) {
			storageURL := (&url.URL{Scheme: "s3", Host: "bucket", Path: "/" + prefix}).String()
			objects, err := openS3Data(t.Context(), storageURL)
			if err != nil {
				t.Fatal(err)
			}
			for _, method := range []string{http.MethodPut, http.MethodGet} {
				key := "teams/acme/pending/ticket"
				if method == http.MethodGet {
					key = "teams/acme/cas/" + strings.Repeat("a", 64)
				}
				signed, err := objects.Sign(t.Context(), capability.ObjectToSign{Method: method, Key: key, Size: 1, SHA256: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				u, err := url.Parse(signed.URL)
				if err != nil || u.Path != "/bucket/"+prefix+key {
					t.Fatalf("%s prefix %q: %s: %v", method, prefix, signed.URL, err)
				}
			}
		})
	}
}

func TestFilesystemDataStartupRequiresOriginForWildcard(t *testing.T) {
	for _, address := range []string{":4344", "0.0.0.0:4344", "[::]:4344", "[0:0:0:0:0:0:0:0]:4344"} {
		t.Run(address, func(t *testing.T) {
			root := t.TempDir()
			objects, err := openFilesystemData(root, "", address, filesystemTestLogNow)
			if err == nil {
				_ = objects.Close()
				t.Fatal("wildcard listener advertised a local transfer URL")
			}
			if !strings.Contains(err.Error(), "--external-url") {
				t.Fatalf("unclear refusal: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "data-signing.key")); !os.IsNotExist(err) {
				t.Fatalf("refused configuration changed signing key: %v", err)
			}
		})
	}
}

func TestFilesystemWildcardRefusesBeforeOpeningStore(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	if err := run([]string{"--addr=:0"}); err == nil || !strings.Contains(err.Error(), "--external-url") {
		t.Fatalf("wildcard --addr: %v", err)
	}
	p, err := paths.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.StateDB()); !os.IsNotExist(err) {
		t.Fatalf("wildcard listener opened the store: %v", err)
	}
}

func TestFilesystemDataUsesStoreLogClock(t *testing.T) {
	now := time.Now().Add(time.Hour)
	st, err := teststore.Open(t, filepath.Join(t.TempDir(), "state.db"), teststore.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	objects, err := openFilesystemData(t.TempDir(), "http://local", "", st.LogUploadNow)
	if err != nil {
		t.Fatal(err)
	}
	defer objects.Close()
	key := "logs/r90/teams/acme/runs/run-1/units/plan/g1/manifest.json"
	digest := blob.Digest(fmt.Sprintf("%x", sha256.Sum256([]byte("x"))))
	if _, _, err := objects.SignLogPut(t.Context(), key, 1, digest, now.Add(limits.LogUploadURLTTL)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := objects.SignLogPut(t.Context(), key, 1, digest, now.Add(limits.LogUploadURLTTL+time.Nanosecond)); err == nil {
		t.Fatal("signed beyond lifetime ceiling")
	}
}

func TestFilesystemDataRequiresLogClock(t *testing.T) {
	root := t.TempDir()
	objects, err := openFilesystemData(root, "http://local", "", nil)
	if objects != nil {
		_ = objects.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "log clock") {
		t.Fatalf("nil log clock = %v, want clock configuration error", err)
	}
	if _, err := os.Stat(filepath.Join(root, "data-signing.key")); !os.IsNotExist(err) {
		t.Fatalf("refused clock configuration changed signing key: %v", err)
	}
}

func filesystemTestLogNow(context.Context) (time.Time, error) {
	return time.Now(), nil
}
