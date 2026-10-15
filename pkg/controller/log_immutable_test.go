package controller

import (
	"bytes"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestSignedLogKeyCannotBeReplaced(t *testing.T) {
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "https://controller.invalid", SigningKey: bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	s := New(nil, nil).WithDataObjects(objects)
	_, router := s.routers(s.handleFinishRun)
	index := 0
	property := func(first, second [32]byte) bool {
		index++
		second[0] = first[0] ^ 1
		subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "team", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogTrigger, Generation: int64(index)}}
		key, err := blob.LogObjectKey(subject, "r90", 1)
		if err != nil {
			t.Log(err)
			return false
		}
		url, _, err := objects.SignLogPut(t.Context(), key, time.Now().Add(time.Minute))
		if err != nil {
			t.Log(err)
			return false
		}
		put := func(body []byte) int {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPut, url, bytes.NewReader(body)))
			return w.Code
		}
		if put(first[:]) != http.StatusCreated || put(first[:]) != http.StatusCreated || put(second[:]) != http.StatusBadRequest {
			return false
		}
		body, err := objects.Get(t.Context(), key)
		if err != nil {
			t.Log(err)
			return false
		}
		stored, err := io.ReadAll(body)
		closeErr := body.Close()
		return err == nil && closeErr == nil && bytes.Equal(stored, first[:])
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64, Rand: rand.New(rand.NewSource(7))}); err != nil {
		t.Fatal(err)
	}
}
