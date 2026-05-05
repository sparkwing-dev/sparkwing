package logs

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SearchResult mirrors web/src/lib/api.ts:LogSearchResult. One
// matching line with its coordinates.
type SearchResult struct {
	RunID   string `json:"run_id"`
	NodeID  string `json:"node_id"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

// SearchResponse matches the dashboard's LogSearchResponse shape.
type SearchResponse struct {
	Query   string         `json:"query"`
	Results []SearchResult `json:"results"`
	Total   int            `json:"total"`
}

// handleSearch greps every log file under root for the `q` param.
// Case-insensitive substring match; no regex (yet). Supports
// optional run_id / node_id prefix filters to narrow the file scan.
// limit defaults to 100 and caps at 500 to keep responses bounded.
//
// This is a v0 implementation: walks the filesystem on every query,
// no index. Ships fast and handles log volumes sparkwing currently
// generates. Revisit with a real index (SQLite FTS or bleve) when
// latency gets noticeable.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeLogsErr(w, http.StatusBadRequest, "q is required")
		return
	}
	needle := strings.ToLower(q)

	runFilter := r.URL.Query().Get("run_id")
	nodeFilter := r.URL.Query().Get("node_id")

	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 500 {
		limit = 500
	}

	resp := SearchResponse{Query: q, Results: []SearchResult{}}

	runsDir := filepath.Join(s.root, "runs")
	if _, err := os.Stat(runsDir); err != nil {
		// Empty logs volume: return empty results rather than 500.
		writeJSONResponse(w, http.StatusOK, resp)
		return
	}

	// Walk runs/<runID>/<nodeID>.log. Skip dirs that don't match
	// the run_id filter so we avoid opening their children.
	err := filepath.WalkDir(runsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable paths; don't kill the walk
		}
		if d.IsDir() {
			if path == runsDir {
				return nil
			}
			if runFilter != "" && filepath.Base(path) != runFilter {
				// Skip unrelated run dirs when a filter is set. Only
				// applies at the one-level-deep run-dir.
				rel, _ := filepath.Rel(runsDir, path)
				if !strings.Contains(rel, string(filepath.Separator)) {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".log") {
			return nil
		}
		// path is runs/<runID>/<nodeID>.log
		rel, rerr := filepath.Rel(runsDir, path)
		if rerr != nil {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 2 {
			return nil
		}
		runID := parts[0]
		nodeID := strings.TrimSuffix(parts[1], ".log")
		if runFilter != "" && runID != runFilter {
			return nil
		}
		if nodeFilter != "" && nodeID != nodeFilter {
			return nil
		}

		if len(resp.Results) >= limit {
			// Keep counting totals but stop collecting once we hit
			// the limit. Return early to reduce file I/O.
			return fs.SkipAll
		}
		f, oerr := os.Open(path)
		if oerr != nil {
			return nil
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if !strings.Contains(strings.ToLower(line), needle) {
				continue
			}
			resp.Total++
			if len(resp.Results) < limit {
				resp.Results = append(resp.Results, SearchResult{
					RunID:   runID,
					NodeID:  nodeID,
					Line:    lineNo,
					Content: line,
				})
			}
		}
		return nil
	})
	if err != nil {
		writeLogsErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSONResponse(w, http.StatusOK, resp)
}

func writeJSONResponse(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeLogsErr(w http.ResponseWriter, status int, msg string) {
	writeJSONResponse(w, status, map[string]string{"error": msg})
}
