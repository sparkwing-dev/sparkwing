package sparkwinglogs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/conformance"
)

// TestConformance_LogStore wires the shared conformance suite
// against an in-process stub of the sparkwing-logs HTTP service.
// The stub honors tail/head/grep query params so the suite's
// filter subtests exercise the real client query encoding without
// reaching the production service.
func TestConformance_LogStore(t *testing.T) {
	conformance.TestLogStore(t, func() storage.LogStore {
		var mu sync.Mutex
		blobs := map[string][]byte{} // "runID/nodeID" -> bytes

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := strings.TrimPrefix(r.URL.Path, "/api/v1/logs/")
			switch r.Method {
			case http.MethodPost:
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				blobs[path] = append(blobs[path], body...)
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
			case http.MethodGet:
				mu.Lock()
				defer mu.Unlock()
				// /stream subpath: return whatever bytes exist now, no
				// SSE framing. The real sparkwing-logs server serves SSE
				// here; the conformance Stream subtest is happy to see
				// existing content as long as the channel surfaces
				// something.
				if strings.HasSuffix(path, "/stream") {
					key := strings.TrimSuffix(path, "/stream")
					_, _ = w.Write(blobs[key])
					return
				}
				if !strings.Contains(path, "/") { // ReadRun
					var buf []byte
					for k, v := range blobs {
						if strings.HasPrefix(k, path+"/") {
							buf = append(buf, []byte("=== "+k+" ===\n")...)
							buf = append(buf, v...)
						}
					}
					_, _ = w.Write(buf)
					return
				}
				body := blobs[path]
				body = applyServerSideFilters(body, r.URL.Query())
				_, _ = w.Write(body)
			case http.MethodDelete:
				mu.Lock()
				for k := range blobs {
					if k == path || strings.HasPrefix(k, path+"/") {
						delete(blobs, k)
					}
				}
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		}))
		t.Cleanup(srv.Close)

		return New(srv.URL, nil, "")
	})
}

// applyServerSideFilters mimics the sparkwing-logs server's
// tail/head/grep handling so the stub round-trips the same filter
// semantics the production server does.
func applyServerSideFilters(body []byte, q map[string][]string) []byte {
	text := string(body)
	if g := first(q["grep"]); g != "" {
		var keep []string
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, g) {
				keep = append(keep, line)
			}
		}
		text = strings.Join(keep, "\n")
	}
	if t := first(q["tail"]); t != "" {
		if n, err := strconv.Atoi(t); err == nil && n > 0 {
			lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
			if n < len(lines) {
				lines = lines[len(lines)-n:]
			}
			text = strings.Join(lines, "\n") + "\n"
		}
	}
	if h := first(q["head"]); h != "" {
		if n, err := strconv.Atoi(h); err == nil && n > 0 {
			lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
			if n < len(lines) {
				lines = lines[:n]
			}
			text = strings.Join(lines, "\n") + "\n"
		}
	}
	return []byte(text)
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}
