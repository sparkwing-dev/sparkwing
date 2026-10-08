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

	"github.com/sparkwing-dev/sparkwing/internal/directdata"
)

func TestSupervisorArtifactsPreferAnnouncedDirectUpload(t *testing.T) {
	t.Setenv(ArtifactStoreEnvVar, "")
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

func TestDirectArtifactEmptyBlobNeedsNoUpload(t *testing.T) {
	var calls atomic.Int32
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "direct uploads carry at least one byte", http.StatusBadRequest)
	}))
	defer controller.Close()
	s := directArtifactStore{client: directdata.New(controller.URL, "grant", "run", nil)}
	ctx := context.Background()
	if err := s.Put(ctx, emptyArtifactBlobKey, bytes.NewReader(nil)); err != nil {
		t.Fatalf("Put empty blob: %v", err)
	}
	if ok, err := s.Has(ctx, emptyArtifactBlobKey); err != nil || !ok {
		t.Fatalf("Has empty blob = %v, %v", ok, err)
	}
	rc, err := s.Get(ctx, emptyArtifactBlobKey)
	if err != nil {
		t.Fatalf("Get empty blob: %v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty blob read = %q, %v", got, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("the controller saw %d requests for an empty blob", calls.Load())
	}
}
