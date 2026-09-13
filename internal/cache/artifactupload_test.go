package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func ceilingFixture(t *testing.T, cfg objectguard.CeilingConfig) {
	t.Helper()
	previous := storeCeiling
	oldArtifacts, oldCache, oldUploads := artifactsDir, cacheDir, uploadsDir
	artifactsDir, cacheDir, uploadsDir = t.TempDir(), t.TempDir(), t.TempDir()
	cfg.Subject, cfg.Remedy = storeCeilingSubject, storeCeilingRemedy
	storeCeiling = objectguard.NewCeiling(cfg)
	t.Cleanup(func() {
		storeCeiling = previous
		artifactsDir, cacheDir, uploadsDir = oldArtifacts, oldCache, oldUploads
	})
}

func TestStoreCeilingThawRouteLetsWritesThroughUntilTheNextMeasurement(t *testing.T) {
	ceilingFixture(t, objectguard.CeilingConfig{
		Limit:     objectguard.CeilingLimit{MaxBytes: 16},
		Reconcile: time.Hour,
	})
	storeCeiling.Observe(objectguard.Usage{Bytes: 4096, Objects: 3})

	refused := httptest.NewRecorder()
	handleCache(refused, httptest.NewRequest(http.MethodPut, "/cache/deps-abc123", strings.NewReader("archive")))
	if refused.Code != http.StatusInsufficientStorage {
		t.Fatalf("status %d before the thaw, want 507", refused.Code)
	}

	w := httptest.NewRecorder()
	handleStoreCeilingThaw(w, httptest.NewRequest(http.MethodPost, "/admin/store-ceiling/thaw", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("thaw status %d, want 200: %s", w.Code, w.Body.String())
	}
	var body struct {
		Thawed  bool `json:"thawed"`
		Ceiling struct {
			Frozen bool `json:"frozen"`
			Thawed bool `json:"thawed"`
		} `json:"store_ceiling"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode thaw response: %v", err)
	}
	if !body.Thawed || body.Ceiling.Frozen || !body.Ceiling.Thawed {
		t.Errorf("the thaw response reports %+v", body)
	}

	accepted := httptest.NewRecorder()
	handleCache(accepted, httptest.NewRequest(http.MethodPut, "/cache/deps-abc123", strings.NewReader("archive")))
	if accepted.Code != http.StatusCreated {
		t.Fatalf("status %d after the thaw, want 201: %s", accepted.Code, accepted.Body.String())
	}
}

func TestStoreCeilingThawIsRefusedWithNoMeasurementScheduled(t *testing.T) {
	ceilingFixture(t, objectguard.CeilingConfig{Limit: objectguard.CeilingLimit{MaxBytes: 16}})
	storeCeiling.Observe(objectguard.Usage{Bytes: 4096, Objects: 3})

	w := httptest.NewRecorder()
	handleStoreCeilingThaw(w, httptest.NewRequest(http.MethodPost, "/admin/store-ceiling/thaw", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d for a thaw nothing would end, want 409", w.Code)
	}
	if !storeCeiling.Frozen() {
		t.Error("the refused thaw cleared the freeze anyway")
	}
}

func TestStoreCeilingMeasureRouteThawsAfterSpaceIsFreed(t *testing.T) {
	ceilingFixture(t, objectguard.CeilingConfig{
		Limit:     objectguard.CeilingLimit{MaxBytes: 32},
		Reconcile: time.Hour,
	})
	blob := filepath.Join(cacheDir, "big.tar.gz")
	if err := os.WriteFile(blob, []byte(strings.Repeat("x", 128)), 0o644); err != nil {
		t.Fatal(err)
	}
	measureStore(t.Context())
	if !storeCeiling.Frozen() {
		t.Fatalf("the store did not freeze on a full volume: %+v", storeCeiling.State())
	}

	if err := os.Remove(blob); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handleStoreCeilingMeasure(w, httptest.NewRequest(http.MethodPost, "/admin/store-ceiling/measure", nil))
	if w.Code != http.StatusAccepted {
		t.Fatalf("measure status %d, want 202: %s", w.Code, w.Body.String())
	}
	var body struct {
		Measuring bool `json:"measuring"`
		Started   bool `json:"started"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode measure response: %v", err)
	}
	if !body.Measuring || !body.Started {
		t.Errorf("the measure response reports %+v", body)
	}
	waitUntil(t, "the store thaws after the volume is emptied", func() bool { return !storeCeiling.Frozen() })
}

func waitUntil(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting until %s", what)
}

func TestStoreCeilingAdminRoutesNeedTheBearerToken(t *testing.T) {
	oldToken := apiToken
	apiToken = "sw-secret"
	t.Cleanup(func() { apiToken = oldToken })

	for path, handler := range map[string]http.HandlerFunc{
		"/admin/store-ceiling/thaw":    handleStoreCeilingThaw,
		"/admin/store-ceiling/measure": handleStoreCeilingMeasure,
	} {
		w := httptest.NewRecorder()
		requireToken(handler)(w, httptest.NewRequest(http.MethodPost, path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s answered %d without a token, want 401", path, w.Code)
		}
	}
}

func TestStoreCeilingAdminRoutesRefuseAGet(t *testing.T) {
	ceilingFixture(t, objectguard.CeilingConfig{Reconcile: time.Hour})
	for path, handler := range map[string]http.HandlerFunc{
		"/admin/store-ceiling/thaw":    handleStoreCeilingThaw,
		"/admin/store-ceiling/measure": handleStoreCeilingMeasure,
	} {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s answered %d to a GET, want 405", path, w.Code)
		}
	}
}

func TestHealthCarriesTheStoreCeiling(t *testing.T) {
	oldProxy := proxyDir
	proxyDir = t.TempDir()
	t.Cleanup(func() { proxyDir = oldProxy })
	ceilingFixture(t, objectguard.CeilingConfig{
		Limit:     objectguard.CeilingLimit{MaxBytes: 64, WarnBytes: 16},
		Reconcile: time.Hour,
	})
	storeCeiling.Observe(objectguard.Usage{Bytes: 32, Objects: 2})

	w := httptest.NewRecorder()
	handleHealthCombined(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	var body struct {
		Status   string         `json:"status"`
		Problems []string       `json:"problems"`
		Ceiling  map[string]any `json:"store_ceiling"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if body.Ceiling["enforced"] != true || body.Ceiling["warning"] != true || body.Ceiling["frozen"] != false {
		t.Errorf("a store past its warning mark reports %v", body.Ceiling)
	}
	if body.Ceiling["bytes"].(float64) != 32 || body.Ceiling["objects"].(float64) != 2 {
		t.Errorf("health reports %v bytes / %v objects, want 32/2", body.Ceiling["bytes"], body.Ceiling["objects"])
	}
	if _, ok := body.Ceiling["reconciled_at"]; !ok {
		t.Error("health reports no reconciled_at after a measurement")
	}
	if body.Status != "degraded" {
		t.Errorf("status = %q past the warning mark, want degraded", body.Status)
	}

	storeCeiling.Observe(objectguard.Usage{Bytes: 4096, Objects: 9})
	frozen := httptest.NewRecorder()
	handleHealthCombined(frozen, httptest.NewRequest(http.MethodGet, "/health", nil))
	if !strings.Contains(frozen.Body.String(), "uploads are refused") {
		t.Errorf("health does not name the freeze: %s", frozen.Body.String())
	}
}

func TestOverwritingAnObjectCountsTheDifferenceNotASecondObject(t *testing.T) {
	ceilingFixture(t, objectguard.CeilingConfig{
		Limit:     objectguard.CeilingLimit{MaxBytes: 1 << 20, MaxObjects: 100},
		Reconcile: time.Hour,
	})

	for _, body := range []string{"12345678", "1234"} {
		w := httptest.NewRecorder()
		handleCache(w, httptest.NewRequest(http.MethodPut, "/cache/deps-abc123", strings.NewReader(body)))
		if w.Code != http.StatusCreated {
			t.Fatalf("put %q: status %d", body, w.Code)
		}
	}

	state := storeCeiling.State()
	if state.Objects != 1 {
		t.Errorf("two writes to one key counted %d objects, want 1", state.Objects)
	}
	if state.Bytes != 4 {
		t.Errorf("the key counted %d bytes, want the 4 it now holds", state.Bytes)
	}
}

func TestStoreMeasurementStopsWhenItsContextDoes(t *testing.T) {
	ceilingFixture(t, objectguard.CeilingConfig{
		Limit:     objectguard.CeilingLimit{MaxBytes: 1 << 20},
		Reconcile: time.Hour,
	})
	if err := os.WriteFile(filepath.Join(cacheDir, "a.tar.gz"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	measureStore(ctx)

	state := storeCeiling.State()
	if !state.Incomplete {
		t.Error("an abandoned walk did not mark the ceiling incomplete")
	}
	if !state.ReconciledAt.IsZero() {
		t.Error("a partial walk was folded in as a measurement")
	}
}

func TestStoreCeilingThawRefusalNamesTheMeasureRoute(t *testing.T) {
	ceilingFixture(t, objectguard.CeilingConfig{Limit: objectguard.CeilingLimit{MaxBytes: 16}})
	storeCeiling.Observe(objectguard.Usage{Bytes: 4096, Objects: 3})

	w := httptest.NewRecorder()
	handleStoreCeilingThaw(w, httptest.NewRequest(http.MethodPost, "/admin/store-ceiling/thaw", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "/admin/store-ceiling/measure") {
		t.Errorf("the refusal %q does not name the route that does work", w.Body.String())
	}
}

func TestMeasureRequestDuringAWalkIsNotDropped(t *testing.T) {
	ceilingFixture(t, objectguard.CeilingConfig{
		Limit:     objectguard.CeilingLimit{MaxBytes: 64},
		Reconcile: time.Hour,
	})
	// safety: a wide tree makes the first walk long enough that the second request
	// lands inside it, which is the case a dropped request would lose.
	for i := range 2000 {
		name := filepath.Join(cacheDir, fmt.Sprintf("filler-%04d.tar.gz", i))
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	blob := filepath.Join(cacheDir, "big.tar.gz")
	if err := os.WriteFile(blob, []byte(strings.Repeat("x", 256)), 0o644); err != nil {
		t.Fatal(err)
	}
	measureStore(t.Context())
	if !storeCeiling.Frozen() {
		t.Fatalf("the store did not freeze: %+v", storeCeiling.State())
	}

	first := httptest.NewRecorder()
	handleStoreCeilingMeasure(first, httptest.NewRequest(http.MethodPost, "/admin/store-ceiling/measure", nil))

	for _, name := range []string{blob} {
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 2000 {
		if err := os.Remove(filepath.Join(cacheDir, fmt.Sprintf("filler-%04d.tar.gz", i))); err != nil {
			t.Fatal(err)
		}
	}
	second := httptest.NewRecorder()
	handleStoreCeilingMeasure(second, httptest.NewRequest(http.MethodPost, "/admin/store-ceiling/measure", nil))
	if second.Code != http.StatusAccepted {
		t.Fatalf("second measure status %d, want 202", second.Code)
	}

	waitUntil(t, "the emptied store thaws", func() bool { return !storeCeiling.Frozen() })
}
