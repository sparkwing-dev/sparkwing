package web

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const compressibleChunkPath = "_next/static/chunks/big.js"

func compressibleChunk() []byte {
	return bytes.Repeat([]byte("export const sparkwingDashboardChunk = 1;\n"), 400)
}

type countingFS struct {
	fs.FS
	reads map[string]*atomic.Int64
}

func (c countingFS) ReadFile(name string) ([]byte, error) {
	if counter, ok := c.reads[name]; ok {
		counter.Add(1)
	}
	return fs.ReadFile(c.FS, name)
}

func compressionBundle() fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(
			"<html><head><title>sparkwing</title></head><body>" +
				strings.Repeat("<p>dashboard shell</p>", 200) + "</body></html>")},
		compressibleChunkPath:            &fstest.MapFile{Data: compressibleChunk()},
		"_next/static/media/inter.woff2": &fstest.MapFile{Data: bytes.Repeat([]byte{0x77, 0x4f, 0x46, 0x32}, 800)},
		"_next/static/chunks/tiny.js":    &fstest.MapFile{Data: []byte("export const a=1;\n")},
	}
}

func requestBundle(t *testing.T, bundle fs.FS, target string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for name, values := range header {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	rec := httptest.NewRecorder()
	HandlerFromOptionsWithBundle(HandlerOptions{}, bundle).ServeHTTP(rec, req)
	return rec
}

func gunzip(t *testing.T, body []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("response is labeled gzip but does not decode: %v", err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip body truncated: %v", err)
	}
	return raw
}

func TestBundleAssetsAreGzipEncodedWhenTheClientAcceptsIt(t *testing.T) {
	raw := compressibleChunk()
	rec := requestBundle(t, compressionBundle(), "/"+compressibleChunkPath,
		http.Header{"Accept-Encoding": {"gzip, deflate, br"}})

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding %q, want gzip", got)
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Errorf("Vary %q does not name Accept-Encoding, so a cache may hand the wrong body to the next client",
			rec.Header().Get("Vary"))
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") &&
		!strings.HasPrefix(got, "application/javascript") {
		t.Errorf("Content-Type %q, want a JavaScript type", got)
	}
	body := rec.Body.Bytes()
	if len(body) >= len(raw) {
		t.Fatalf("encoded body is %d bytes against %d raw, so the encoding saved nothing", len(body), len(raw))
	}
	if !bytes.Equal(gunzip(t, body), raw) {
		t.Error("the decoded body does not match the file in the bundle")
	}
	if want := strconv.Itoa(len(body)); rec.Header().Get("Content-Length") != want {
		t.Errorf("Content-Length %q, want %q", rec.Header().Get("Content-Length"), want)
	}
}

func TestBundleAssetsStayUnencodedForAClientThatDoesNotAcceptGzip(t *testing.T) {
	for _, header := range []http.Header{
		{},
		{"Accept-Encoding": {"br"}},
		{"Accept-Encoding": {"gzip;q=0"}},
		{"Accept-Encoding": {"gzip;q=0, *"}},
	} {
		t.Run(strings.Join(header["Accept-Encoding"], ","), func(t *testing.T) {
			rec := requestBundle(t, compressionBundle(), "/"+compressibleChunkPath, header)
			if got := rec.Header().Get("Content-Encoding"); got != "" {
				t.Fatalf("Content-Encoding %q, want none", got)
			}
			if !bytes.Equal(rec.Body.Bytes(), compressibleChunk()) {
				t.Error("the unencoded response does not match the file in the bundle")
			}
		})
	}
}

func TestRangeRequestsAreServedUnencoded(t *testing.T) {
	rec := requestBundle(t, compressionBundle(), "/"+compressibleChunkPath, http.Header{
		"Accept-Encoding": {"gzip"},
		"Range":           {"bytes=0-9"},
	})
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status %d, want 206; encoding a ranged request breaks the offsets it names", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding %q on a ranged response, want none", got)
	}
	if got, want := rec.Body.Bytes(), compressibleChunk()[:10]; !bytes.Equal(got, want) {
		t.Errorf("ranged body %q, want %q", got, want)
	}
}

func TestAlreadyCompressedAssetsAreLeftAlone(t *testing.T) {
	rec := requestBundle(t, compressionBundle(), "/_next/static/media/inter.woff2",
		http.Header{"Accept-Encoding": {"gzip"}})
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding %q on a font, want none", got)
	}
}

func TestSmallAssetsAreLeftAlone(t *testing.T) {
	rec := requestBundle(t, compressionBundle(), "/_next/static/chunks/tiny.js",
		http.Header{"Accept-Encoding": {"gzip"}})
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding %q on an 18-byte file, want none", got)
	}
}

func TestGeneratedPagesAreGzipEncoded(t *testing.T) {
	bundle := compressionBundle()
	shell := bundle["index.html"].Data

	rec := requestBundle(t, bundle, "/", http.Header{"Accept-Encoding": {"gzip"}})
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding %q on the app shell, want gzip", got)
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Errorf("Vary %q does not name Accept-Encoding", rec.Header().Get("Vary"))
	}
	if got := gunzip(t, rec.Body.Bytes()); !bytes.Equal(got, shell) {
		t.Errorf("the decoded shell differs from the bundle by %d bytes", len(got)-len(shell))
	}

	plain := requestBundle(t, bundle, "/", nil)
	if got := plain.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding %q without Accept-Encoding, want none", got)
	}
	if !bytes.Equal(plain.Body.Bytes(), shell) {
		t.Error("the unencoded shell does not match the bundle")
	}
}

func TestEachBundleAssetIsEncodedOnce(t *testing.T) {
	reads := map[string]*atomic.Int64{compressibleChunkPath: {}}
	bundle := countingFS{FS: compressionBundle(), reads: reads}
	handler := HandlerFromOptionsWithBundle(HandlerOptions{}, bundle)

	const requests = 5
	for range requests {
		req := httptest.NewRequest(http.MethodGet, "/"+compressibleChunkPath, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("request returned Content-Encoding %q, want gzip", rec.Header().Get("Content-Encoding"))
		}
	}
	if got := reads[compressibleChunkPath].Load(); got != 1 {
		t.Errorf("the bundle read the chunk %d times over %d requests, want 1; "+
			"the encoding is being recomputed per request", got, requests)
	}
}

func TestEventStreamsAreNeverEncoded(t *testing.T) {
	if compressibleType("text/event-stream") {
		t.Error("an event stream must never be treated as compressible: the encoder buffers, " +
			"and a buffered stream stops being live")
	}

	backend := &fakeBackend{
		getRun:     func(string) (*store.Run, error) { return &store.Run{ID: "r1", Status: "success"}, nil },
		listEvents: func(string, int64, int) ([]store.Event, error) { return nil, nil },
	}
	srv := httptest.NewServer(HandlerFromOptionsWithBundle(
		HandlerOptions{Backend: backend}, compressionBundle()))
	t.Cleanup(srv.Close)

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/runs/r1/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("event stream request: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding %q on the event stream, want none", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read event stream: %v", err)
	}
	if !strings.Contains(string(body), "stream_end") {
		t.Errorf("event stream did not reach its end marker: %q", body)
	}
}

func TestAcceptsGzipReadsQualityValues(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"gzip", true},
		{"gzip, deflate, br", true},
		{"gzip;q=1.0", true},
		{"GZIP", true},
		{" gzip ; q=0.5 ", true},
		{"*", true},
		{"deflate, *;q=0.5", true},
		{"", false},
		{"br", false},
		{"gzip;q=0", false},
		{"gzip;q=0.0", false},
		{"*;q=0", false},
		{"*, gzip;q=0", false},
		{"identity", false},
	}
	for _, tc := range cases {
		t.Run(tc.header, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				req.Header.Set("Accept-Encoding", tc.header)
			}
			if got := acceptsGzip(req); got != tc.want {
				t.Errorf("acceptsGzip(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}
}

func TestCompressibleTypeCoversTheBundlesMediaTypes(t *testing.T) {
	compressible := []string{
		"text/html; charset=utf-8",
		"text/javascript; charset=utf-8",
		"application/javascript",
		"text/css; charset=utf-8",
		"application/json",
		"image/svg+xml",
		"text/plain; charset=utf-8",
	}
	for _, ct := range compressible {
		if !compressibleType(ct) {
			t.Errorf("compressibleType(%q) = false, want true", ct)
		}
	}
	for _, ct := range []string{"", "font/woff2", "image/png", "video/mp4", "application/zip", "text/event-stream"} {
		if compressibleType(ct) {
			t.Errorf("compressibleType(%q) = true, want false", ct)
		}
	}
}
