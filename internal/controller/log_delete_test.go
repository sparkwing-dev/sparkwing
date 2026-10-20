package controller

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestDataStorageTeamPurgeDeletesLateSignedLogs(t *testing.T) {
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "https://controller.invalid", SigningKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	server := New(nil, nil).WithDataObjects(objects)
	key, err := blob.LogObjectKey(blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "acme", RunID: "run-1", Unit: &blob.LogUnit{Kind: blob.LogPlan, Generation: 1}}, "r365", 1)
	if err != nil {
		t.Fatal(err)
	}
	signed, _, err := objects.SignLogPut(t.Context(), key, 8, blob.Digest(fmt.Sprintf("%x", sha256.Sum256([]byte("late log")))), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	put := func() {
		t.Helper()
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPut, signed, strings.NewReader("late log")))
		if response.Code != http.StatusCreated {
			t.Fatalf("signed PUT = %d %s", response.Code, response.Body)
		}
	}
	for range 2 {
		put()
		body, err := objects.Get(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(body)
		if err = errors.Join(err, body.Close()); err != nil || string(raw) != "late log" {
			t.Fatalf("read = %q, %v", raw, err)
		}
		if err := server.purgeTeamLogs(t.Context(), "acme"); err != nil {
			t.Fatal(err)
		}
		if body, err := objects.Get(t.Context(), key); !errors.Is(err, blob.ErrNotFound) {
			if body != nil {
				_ = body.Close()
			}
			t.Fatalf("late log survived purge: %v", err)
		}
	}
}

type failedLogDeletion struct{ blob.ObjectBackend }

func (failedLogDeletion) DeleteLogs(_ context.Context, _, _ string) (int64, error) {
	return 0, errors.New("object service unavailable")
}

func TestDataStorageDeletionFailsLoudly(t *testing.T) {
	for _, objects := range []blob.ObjectBackend{failedLogDeletion{}, failedDataDeletion{}} {
		if _, err := New(nil, nil).WithDataObjects(objects).deleteDataLogs(t.Context(), "acme", "run"); err == nil {
			t.Fatal("backend without working log deletion succeeded")
		}
	}
}
