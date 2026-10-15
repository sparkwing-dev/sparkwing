package main

import (
	"bytes"
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
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

func TestFilesystemDataStartup(t *testing.T) {
	for _, fixture := range []struct{ external, address, host string }{
		{"", "127.0.0.1:4344", "127.0.0.1:4344"},
		{"", ":4344", "localhost:4344"},
		{"", "0.0.0.0:4344", "localhost:4344"},
		{"", "[::]:4344", "localhost:4344"},
		{"", "[::1]:4344", "[::1]:4344"},
		{"https://controller.invalid", "0.0.0.0:4344", "controller.invalid"},
		{"https://controller.invalid/prefix", "0.0.0.0:4344", "controller.invalid"},
	} {
		t.Run(fixture.host+fixture.address, func(t *testing.T) {
			root := t.TempDir()
			objects, err := openFilesystemData(root, fixture.external, fixture.address)
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
		objects, err := openFilesystemData(t.TempDir(), fixture.external, fixture.address)
		if err == nil {
			if closeErr := objects.Close(); closeErr != nil {
				t.Error(closeErr)
			}
			t.Fatal("invalid configuration opened a backend")
		}
	}
}
