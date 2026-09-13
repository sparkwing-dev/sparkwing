package cache

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
)

type truncatedBody struct {
	sent  int
	limit int
}

func (b *truncatedBody) Read(p []byte) (int, error) {
	if b.sent >= b.limit {
		return 0, io.ErrUnexpectedEOF
	}
	n := min(len(p), b.limit-b.sent)
	for i := range n {
		p[i] = 'x'
	}
	b.sent += n
	return n, nil
}

func TestArtifactUploadLeavesNoFileWhenTheClientDiesMidBody(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	t.Cleanup(func() { artifactsDir = oldDir })

	req := httptest.NewRequest(http.MethodPost, "/artifacts/job123?path=out.tar", &truncatedBody{limit: 4096})
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	if w.Code < 400 {
		t.Errorf("status %d, want a failure for a truncated body", w.Code)
	}

	jobDir := filepath.Join(artifactsDir, "job123")
	entries, err := os.ReadDir(jobDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("aborted upload left %q behind in the job directory", e.Name())
	}
}

func TestArtifactUploadRejectsABodyOverTheCap(t *testing.T) {
	oldDir, oldMax := artifactsDir, maxArtifactBytes
	artifactsDir, maxArtifactBytes = t.TempDir(), 1<<10
	t.Cleanup(func() { artifactsDir, maxArtifactBytes = oldDir, oldMax })

	req := httptest.NewRequest(http.MethodPost, "/artifacts/job123?path=big.bin",
		io.LimitReader(neverEndingReader{}, maxArtifactBytes+1))
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d, want 413 for a body over the cap", w.Code)
	}
	if _, err := os.Stat(filepath.Join(artifactsDir, "job123", "big.bin")); !os.IsNotExist(err) {
		t.Errorf("an over-cap upload must not leave the artifact behind: %v", err)
	}
}

type neverEndingReader struct{}

func (neverEndingReader) Read(p []byte) (int, error) { return len(p), nil }

func TestArtifactUploadStagingFilesAreNotListedOrDownloadable(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	t.Cleanup(func() { artifactsDir = oldDir })

	jobDir := filepath.Join(artifactsDir, "job123")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, artifactTempPrefix+"leftover"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/artifacts/job123", nil)
	w := httptest.NewRecorder()
	handleArtifacts(w, req)
	if body := w.Body.String(); strings.Contains(body, artifactTempPrefix) {
		t.Errorf("artifact listing exposed a staging file: %s", strings.TrimSpace(body))
	}

	req = httptest.NewRequest(http.MethodGet, "/artifacts/job123?glob=*", nil)
	w = httptest.NewRecorder()
	handleArtifacts(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("download status %d, want 404: a staging file is not an artifact", w.Code)
	}
}

func TestArtifactUploadIsAtomic(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	t.Cleanup(func() { artifactsDir = oldDir })

	dest := filepath.Join(artifactsDir, "job123", "out.tar")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/artifacts/job123?path=out.tar", &truncatedBody{limit: 4096})
	w := httptest.NewRecorder()
	handleArtifacts(w, req)
	if w.Code < 400 {
		t.Fatalf("status %d, want a failure for a truncated body", w.Code)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("a failed re-upload destroyed the artifact already stored: %v", err)
	}
	if string(data) != "previous" {
		t.Errorf("artifact content %q, want the previously stored copy", data)
	}
}

func TestArtifactUploadRefusesTheReservedStagingPrefix(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	t.Cleanup(func() { artifactsDir = oldDir })

	for _, path := range []string{
		artifactTempPrefix + "report.txt",
		"out/" + artifactTempPrefix + "report.txt",
	} {
		req := httptest.NewRequest(http.MethodPost, "/artifacts/job9?path="+path, strings.NewReader("hi"))
		w := httptest.NewRecorder()
		handleArtifacts(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("path %q: status %d, want 400 for the reserved staging prefix", path, w.Code)
		}
		if _, err := os.Stat(filepath.Join(artifactsDir, "job9", path)); !os.IsNotExist(err) {
			t.Errorf("path %q: a refused upload still landed on disk: %v", path, err)
		}
	}
}

func TestArtifactListReturnsAnEmptyArrayNotNull(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	t.Cleanup(func() { artifactsDir = oldDir })

	jobDir := filepath.Join(artifactsDir, "job123")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, artifactTempPrefix+"leftover"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/artifacts/job123", nil)
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	if got := strings.TrimSpace(w.Body.String()); got != "[]" {
		t.Errorf("body %q, want []", got)
	}
}

func TestCacheArchiveRejectsABodyOverTheCap(t *testing.T) {
	oldDir, oldMax := cacheDir, maxCacheArchiveBytes
	cacheDir, maxCacheArchiveBytes = t.TempDir(), 1<<10
	t.Cleanup(func() { cacheDir, maxCacheArchiveBytes = oldDir, oldMax })

	req := httptest.NewRequest(http.MethodPut, "/cache/deps-abc123",
		io.LimitReader(neverEndingReader{}, maxCacheArchiveBytes+1))
	w := httptest.NewRecorder()
	handleCache(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413 for an archive over the cap", w.Code)
	}
	if !strings.Contains(w.Body.String(), "1024") {
		t.Errorf("the refusal %q does not name the cap", w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "deps-abc123.tar.gz")); !os.IsNotExist(err) {
		t.Errorf("an over-cap archive must not be stored: %v", err)
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("the refused upload left %q behind", e.Name())
	}
}

func TestCacheArchiveAcceptsABodyUnderTheCap(t *testing.T) {
	oldDir, oldMax := cacheDir, maxCacheArchiveBytes
	cacheDir, maxCacheArchiveBytes = t.TempDir(), 1<<10
	t.Cleanup(func() { cacheDir, maxCacheArchiveBytes = oldDir, oldMax })

	req := httptest.NewRequest(http.MethodPut, "/cache/deps-abc123", strings.NewReader("small archive"))
	w := httptest.NewRecorder()
	handleCache(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201 for an archive under the cap", w.Code)
	}
}

func TestArtifactUploadNamesTheCapItRefusedAgainst(t *testing.T) {
	oldDir, oldMax := artifactsDir, maxArtifactBytes
	artifactsDir, maxArtifactBytes = t.TempDir(), 2<<10
	t.Cleanup(func() { artifactsDir, maxArtifactBytes = oldDir, oldMax })

	req := httptest.NewRequest(http.MethodPost, "/artifacts/job123?path=big.bin",
		io.LimitReader(neverEndingReader{}, maxArtifactBytes+1))
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413 for a body over the cap", w.Code)
	}
	if !strings.Contains(w.Body.String(), "2048") {
		t.Errorf("the refusal %q does not name the cap", w.Body.String())
	}
}

func TestUncappedUploadsAreNotRefusedAsZeroByteOnes(t *testing.T) {
	oldArtifacts, oldArtifactMax := artifactsDir, maxArtifactBytes
	oldCache, oldCacheMax := cacheDir, maxCacheArchiveBytes
	artifactsDir, maxArtifactBytes = t.TempDir(), 0
	cacheDir, maxCacheArchiveBytes = t.TempDir(), 0
	t.Cleanup(func() {
		artifactsDir, maxArtifactBytes = oldArtifacts, oldArtifactMax
		cacheDir, maxCacheArchiveBytes = oldCache, oldCacheMax
	})

	body := strings.Repeat("x", 1<<20)
	req := httptest.NewRequest(http.MethodPost, "/artifacts/job123?path=big.bin", strings.NewReader(body))
	w := httptest.NewRecorder()
	handleArtifacts(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("artifact upload status %d with the cap disabled, want 200", w.Code)
	}

	req = httptest.NewRequest(http.MethodPut, "/cache/deps-abc123", strings.NewReader(body))
	w = httptest.NewRecorder()
	handleCache(w, req)
	if w.Code != http.StatusCreated {
		t.Errorf("cache archive status %d with the cap disabled, want 201", w.Code)
	}
}

func frozenStore(t *testing.T, limit objectguard.CeilingLimit, usage objectguard.Usage) {
	t.Helper()
	previous := storeCeiling
	t.Cleanup(func() { storeCeiling = previous })
	storeCeiling = objectguard.NewCeiling(objectguard.CeilingConfig{
		Limit:   limit,
		Subject: storeCeilingSubject,
		Remedy:  storeCeilingRemedy,
	})
	storeCeiling.Observe(usage)
	if !storeCeiling.Frozen() {
		t.Fatalf("the fixture left the store writable: %+v", storeCeiling.State())
	}
}

func TestArtifactUploadIsRefusedWhileTheStoreIsOverItsCeiling(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	t.Cleanup(func() { artifactsDir = oldDir })
	frozenStore(t, objectguard.CeilingLimit{MaxBytes: 1 << 10}, objectguard.Usage{Bytes: 4 << 10, Objects: 3})

	req := httptest.NewRequest(http.MethodPost, "/artifacts/job123?path=out.bin", strings.NewReader("payload"))
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	if w.Code != http.StatusInsufficientStorage {
		t.Fatalf("status %d, want 507 while the store is frozen", w.Code)
	}
	for _, want := range []string{"storage ceiling reached", storeCeilingSubject, "1024", "--max-store-bytes"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("the refusal %q does not name %q", w.Body.String(), want)
		}
	}
	if _, err := os.Stat(filepath.Join(artifactsDir, "job123", "out.bin")); !os.IsNotExist(err) {
		t.Errorf("the refused upload was stored anyway: %v", err)
	}
}

func TestCacheArchivePutIsRefusedWhileTheStoreIsOverItsCeiling(t *testing.T) {
	oldDir := cacheDir
	cacheDir = t.TempDir()
	t.Cleanup(func() { cacheDir = oldDir })
	frozenStore(t, objectguard.CeilingLimit{MaxObjects: 2}, objectguard.Usage{Bytes: 10, Objects: 9})

	req := httptest.NewRequest(http.MethodPut, "/cache/deps-abc123", strings.NewReader("archive"))
	w := httptest.NewRecorder()
	handleCache(w, req)

	if w.Code != http.StatusInsufficientStorage {
		t.Fatalf("status %d, want 507 while the store is frozen", w.Code)
	}
	if !strings.Contains(w.Body.String(), "storage ceiling reached") {
		t.Errorf("the refusal %q does not name the storage ceiling", w.Body.String())
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("the refused archive left %q behind", e.Name())
	}
}

func TestCacheReadsStayOpenWhileTheStoreIsFrozen(t *testing.T) {
	oldDir := cacheDir
	cacheDir = t.TempDir()
	t.Cleanup(func() { cacheDir = oldDir })
	if err := os.WriteFile(filepath.Join(cacheDir, "deps-abc123.tar.gz"), []byte("stored"), 0o644); err != nil {
		t.Fatal(err)
	}
	frozenStore(t, objectguard.CeilingLimit{MaxBytes: 1}, objectguard.Usage{Bytes: 99})

	req := httptest.NewRequest(http.MethodGet, "/cache/deps-abc123", nil)
	w := httptest.NewRecorder()
	handleCache(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d reading from a frozen store, want 200", w.Code)
	}
	if w.Body.String() != "stored" {
		t.Errorf("read returned %q, want the stored archive", w.Body.String())
	}
}

func TestStoreMeasurementCountsWhatTheServiceStored(t *testing.T) {
	oldArtifacts, oldCache, oldUploads := artifactsDir, cacheDir, uploadsDir
	oldCeiling := storeCeiling
	artifactsDir, cacheDir, uploadsDir = t.TempDir(), t.TempDir(), t.TempDir()
	storeCeiling = objectguard.NewCeiling(objectguard.CeilingConfig{
		Limit: objectguard.CeilingLimit{MaxBytes: 1 << 20},
	})
	t.Cleanup(func() {
		artifactsDir, cacheDir, uploadsDir = oldArtifacts, oldCache, oldUploads
		storeCeiling = oldCeiling
	})

	if err := os.WriteFile(filepath.Join(cacheDir, "a.tar.gz"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(artifactsDir, "job1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactsDir, "job1", "out.bin"), []byte("1234567"), 0o644); err != nil {
		t.Fatal(err)
	}

	measureStore(t.Context())
	state := storeCeiling.State()
	if state.Bytes != 12 || state.Objects != 2 {
		t.Errorf("measured %d bytes / %d objects, want 12/2", state.Bytes, state.Objects)
	}
	if state.ReconciledAt.IsZero() {
		t.Error("the measurement recorded no time")
	}
}
