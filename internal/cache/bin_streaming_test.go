package cache

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type observedBinUpload struct {
	t     *testing.T
	first bool
	fail  bool
}

func (r *observedBinUpload) Read(p []byte) (int, error) {
	if !r.first {
		r.first = true
		for i := range 256 {
			p[i] = 'x'
		}
		return 256, nil
	}
	entries, err := os.ReadDir(binsDir)
	if err != nil {
		r.t.Fatal(err)
	}
	staged := false
	for _, entry := range entries {
		info, err := entry.Info()
		if err == nil && info.Size() == 256 {
			staged = true
		}
	}
	if !staged {
		r.t.Error("upload body was buffered instead of persisted before the next read")
	}
	if r.fail {
		return 0, io.ErrUnexpectedEOF
	}
	return 0, io.EOF
}

func TestBinaryUploadStreamsBeforeReadingTheWholeBody(t *testing.T) {
	for _, fail := range []bool{false, true} {
		old := binsDir
		binsDir = t.TempDir()
		func() {
			defer func() { binsDir = old }()
			body := &observedBinUpload{t: t, fail: fail}
			rec := httptest.NewRecorder()
			handleBin(rec, httptest.NewRequest(http.MethodPut, "/bin/deadbeef", body))
			if fail {
				if rec.Code != http.StatusBadRequest {
					t.Errorf("interrupted upload status=%d", rec.Code)
				}
				entries, err := os.ReadDir(binsDir)
				if err != nil || len(entries) != 0 {
					t.Fatalf("failed upload left files: %v %v", entries, err)
				}
			} else {
				if rec.Code != http.StatusCreated {
					t.Fatalf("upload status=%d body=%s", rec.Code, rec.Body)
				}
				data, err := os.ReadFile(filepath.Join(binsDir, "deadbeef"))
				if err != nil || len(data) != 256 {
					t.Fatalf("stored bytes=%d err=%v", len(data), err)
				}
			}
		}()
	}
}
