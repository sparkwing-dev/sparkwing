package orchestrator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestSupervisorArtifactsPreferAnnouncedDirectUpload(t *testing.T) {
	t.Setenv(ArtifactStoreEnvVar, "")
	t.Setenv(DevEnvDisableEnv, "1")
	body := []byte("unknown length artifact")
	sum := sha256.Sum256(body)
	digest := fmt.Sprintf("%x", sum)
	var legacyWrites, committed atomic.Int32
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyWrites.Add(1)
	}))
	defer legacy.Close()
	object := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("bearer reached S3")
		}
		if r.Header.Get("x-amz-checksum-sha256") != base64.StdEncoding.EncodeToString(sum[:]) {
			t.Error("wrong checksum header")
		}
		got, _ := io.ReadAll(r.Body)
		if !bytes.Equal(got, body) {
			t.Errorf("body = %q", got)
		}
	}))
	defer object.Close()
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/data/capabilities":
			_, _ = io.WriteString(w, `{"upload":true,"download":true}`)
		case "/api/v1/data/upload":
			var req struct {
				Key  string `json:"key"`
				Size int64  `json:"size"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.Key != "artifacts/blobs/"+digest || req.Size != int64(len(body)) {
				t.Errorf("reserve = %+v", req)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"upload_id": "u", "url": object.URL,
				"headers": map[string]string{"x-amz-checksum-sha256": base64.StdEncoding.EncodeToString(sum[:])},
			})
		case "/api/v1/data/commit":
			committed.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer controller.Close()
	store, err := supervisorArtifactStore(context.Background(), controller.URL, "run", legacy.URL, "grant")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), "artifacts/blobs/"+digest, bytes.NewBuffer(body)); err != nil {
		t.Fatal(err)
	}
	if committed.Load() != 1 || legacyWrites.Load() != 0 {
		t.Fatalf("commits = %d; legacy writes = %d", committed.Load(), legacyWrites.Load())
	}
}
