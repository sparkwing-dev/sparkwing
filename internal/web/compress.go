package web

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
)

// perf: under a kilobyte the gzip member header and trailer cost more than the
// encoding saves, so small bodies go out as they are.
const minGzipBytes = 1024

// perf: the embedded bundle is fixed for the life of the process, so each file's
// encoding is computed once and every later request writes the stored bytes.
type gzipCache struct {
	entries sync.Map
}

type gzipEntry struct {
	once sync.Once
	body []byte
	ok   bool
}

// perf: load runs only on the first request for a key, because embed.FS copies
// the whole file on every read.
func (c *gzipCache) encoded(key string, load func() ([]byte, error)) ([]byte, bool) {
	stored, _ := c.entries.LoadOrStore(key, &gzipEntry{})
	entry, _ := stored.(*gzipEntry)
	entry.once.Do(func() {
		raw, err := load()
		if err != nil || len(raw) < minGzipBytes {
			return
		}
		body := gzipEncode(raw, gzip.BestCompression)
		if len(body) == 0 || len(body) >= len(raw) {
			return
		}
		entry.body, entry.ok = body, true
	})
	return entry.body, entry.ok
}

func gzipEncode(raw []byte, level int) []byte {
	var buf bytes.Buffer
	buf.Grow(len(raw) / 2)
	zw, err := gzip.NewWriterLevel(&buf, level)
	if err != nil {
		return nil
	}
	if _, err := zw.Write(raw); err != nil {
		return nil
	}
	if err := zw.Close(); err != nil {
		return nil
	}
	return buf.Bytes()
}

// safety: an explicit gzip entry outranks a wildcard and a quality of zero
// refuses the encoding, so a client that says no is never encoded to.
func acceptsGzip(r *http.Request) bool {
	explicit, wildcard := -1.0, -1.0
	for _, field := range r.Header.Values("Accept-Encoding") {
		for _, part := range strings.Split(field, ",") {
			name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
			name = strings.TrimSpace(name)
			switch {
			case strings.EqualFold(name, "gzip"):
				explicit = encodingQuality(params)
			case name == "*":
				wildcard = encodingQuality(params)
			}
		}
	}
	if explicit >= 0 {
		return explicit > 0
	}
	return wildcard > 0
}

func encodingQuality(params string) float64 {
	for _, param := range strings.Split(params, ";") {
		key, value, ok := strings.Cut(param, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "q") {
			continue
		}
		quality, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return 0
		}
		return quality
	}
	return 1
}

// perf: media, fonts and archives arrive compressed, so encoding them again
// spends processor time to add bytes. An event stream is refused outright
// because the encoder buffers, and a buffered event stream is not a live one.
func compressibleType(contentType string) bool {
	base := strings.ToLower(strings.TrimSpace(contentType))
	if semicolon := strings.IndexByte(base, ';'); semicolon >= 0 {
		base = strings.TrimSpace(base[:semicolon])
	}
	if base == "text/event-stream" {
		return false
	}
	if strings.HasPrefix(base, "text/") {
		return true
	}
	switch base {
	case "application/javascript",
		"application/json",
		"application/manifest+json",
		"application/wasm",
		"application/xml",
		"image/svg+xml":
		return true
	}
	return false
}

// safety: a false return must leave the response untouched, because the caller
// then hands the same request to its own file server.
func serveBundleAsset(w http.ResponseWriter, r *http.Request, bundleFS fs.FS, name string, cache *gzipCache) bool {
	// safety: a byte range names an offset in the identity representation, which
	// the encoded body does not share, so a ranged request stays unencoded.
	if r.Header.Get("Range") != "" || !acceptsGzip(r) {
		return false
	}
	contentType := mime.TypeByExtension(path.Ext(name))
	if !compressibleType(contentType) {
		return false
	}
	body, ok := cache.encoded(name, func() ([]byte, error) { return fs.ReadFile(bundleFS, name) })
	if !ok {
		return false
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Encoding", "gzip")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
	return true
}

// safety: these bytes carry a per-request CSP nonce, so no stored encoding can
// serve them and the page is encoded on its way out.
func writeGeneratedHTML(w http.ResponseWriter, r *http.Request, body []byte) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Add("Vary", "Accept-Encoding")
	if acceptsGzip(r) && len(body) >= minGzipBytes {
		if encoded := gzipEncode(body, gzip.DefaultCompression); len(encoded) > 0 && len(encoded) < len(body) {
			h.Set("Content-Encoding", "gzip")
			body = encoded
		}
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}
