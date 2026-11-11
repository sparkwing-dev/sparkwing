package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestArtifactIntegrityRefusalNamesArtifactAndRepair(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusOK} {
		for _, format := range []string{"json", "plain", "pretty"} {
			t.Run(fmt.Sprintf("%s/%d", format, status), func(t *testing.T) {
				const original = "golden"
				digest := sha256.Sum256([]byte(original))
				entry := blob.ManifestEntry{Path: "payload.bin", Size: int64(len(original)), Digest: blob.Digest(hex.EncodeToString(digest[:]))}
				var restored atomic.Bool
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/v1/runs/r1":
						_ = json.NewEncoder(w).Encode(controllerwire.RunDetail{ID: "r1"})
					case "/api/v1/runs/r1/jobs/emit/artifacts":
						_ = json.NewEncoder(w).Encode(controllerwire.JobArtifacts{Artifacts: []blob.ManifestEntry{entry}, Download: &blob.SignedTransfer{URL: "http://" + r.Host + "/blob?signature=private", Size: entry.Size, Digest: entry.Digest}})
					case "/blob":
						if restored.Load() {
							_, _ = io.WriteString(w, original)
							return
						}
						if status == http.StatusOK {
							_, _ = io.WriteString(w, "forged")
							return
						}
						w.WriteHeader(http.StatusConflict)
						_, _ = io.WriteString(w, `{"error":{"code":"conflict","message":"object integrity mismatch"}}`)
					default:
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				reader, err := client.NewArtifactReader(client.EngineOptions{URL: server.URL})
				if err != nil {
					t.Fatal(err)
				}
				dest := filepath.Join(t.TempDir(), "payload.bin")
				inv := logInvocation(t, "artifacts", "--run", "r1", "--job", "emit", "--path", "payload.bin", "--dest", dest, "-o", format)
				failure := observeRunArtifacts(t.Context(), inv, reader, io.Discard)
				if failure == nil || exitCodeFor(failure) == 0 {
					t.Fatal("corrupt artifact was accepted")
				}
				var diagnostic bytes.Buffer
				if err := writeInvocationError(&diagnostic, &invocationError{inv: inv, cause: failure}, hosterr.WriteJSON); err != nil {
					t.Fatal(err)
				}
				for _, want := range []string{"payload.bin", "emit", "r1", "object integrity mismatch", "restore", "trusted", "download"} {
					if !strings.Contains(diagnostic.String(), want) {
						t.Errorf("diagnostic %s omits %q", &diagnostic, want)
					}
				}
				wantCode := "conflict"
				if status == http.StatusOK {
					wantCode = "integrity_failure"
				}
				if format == "json" && !strings.Contains(diagnostic.String(), `"code":"`+wantCode+`"`) {
					t.Errorf("integrity class lost: %s", &diagnostic)
				}
				if strings.Contains(diagnostic.String(), "signature") || strings.Contains(diagnostic.String(), "private") {
					t.Fatal("signed URL escaped the transfer boundary")
				}
				if _, err := os.Stat(dest); !os.IsNotExist(err) {
					t.Fatalf("failed download published bytes: %v", err)
				}
				restored.Store(true)
				if err := observeRunArtifacts(t.Context(), inv, reader, io.Discard); err != nil {
					t.Fatalf("restored artifact cannot be downloaded: %v", err)
				}
				if body, err := os.ReadFile(dest); err != nil || string(body) != original {
					t.Fatalf("restored download = %q, %v", body, err)
				}
			})
		}
	}
}
