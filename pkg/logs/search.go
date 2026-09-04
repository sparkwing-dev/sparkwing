package logs

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
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
// Truncated reports that a byte, time, or cancellation budget stopped
// the scan before it read every matching line.
type SearchResponse struct {
	Query     string         `json:"query"`
	Results   []SearchResult `json:"results"`
	Total     int            `json:"total"`
	Truncated bool           `json:"truncated,omitempty"`
}

const (
	defaultSearchLimit = 100
	maxSearchLimit     = 500
	searchClockEvery   = 512
)

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeLogsErr(w, http.StatusBadRequest, "q is required")
		return
	}

	// safety: without a run id the scan walks every stored run, so an unfiltered query is refused.
	runFilter := r.URL.Query().Get("run_id")
	if runFilter == "" {
		writeLogsErr(w, http.StatusBadRequest, "run_id is required")
		return
	}
	if err := validateID(runFilter); err != nil {
		writeLogsErr(w, http.StatusBadRequest, "run_id: "+err.Error())
		return
	}

	nodeFilter := r.URL.Query().Get("node_id")
	if nodeFilter != "" {
		if err := validateNodeID(nodeFilter); err != nil {
			writeLogsErr(w, http.StatusBadRequest, "node_id: "+err.Error())
			return
		}
		nodeFilter = strings.TrimSuffix(nodeFile(nodeFilter), ".log")
	}

	limit := defaultSearchLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	resp := SearchResponse{Query: q, Results: []SearchResult{}}

	root, err := s.openRunsRoot()
	if err != nil {
		s.storeError(w, "open runs root", err)
		return
	}
	defer func() { _ = root.Close() }()
	entries, err := readRunDir(root, runFilter)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSONResponse(w, http.StatusOK, resp)
			return
		}
		s.storeError(w, "read run dir", err)
		return
	}

	budget := s.newSearchBudget(r.Context())
	needle := strings.ToLower(q)
	groups := nodeLogGroups(root, runFilter, entries)
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		nodeID := strings.TrimSuffix(name, ".log")
		if nodeFilter != "" && nodeID != nodeFilter {
			continue
		}
		if budget.spent() || len(resp.Results) >= limit {
			resp.Truncated = true
			break
		}
		lineNo := 0
		for _, path := range groups[name] {
			f, oerr := root.Open(filepath.Join(runFilter, path))
			if oerr != nil {
				continue
			}
			scanNode(f, needle, runFilter, nodeID, limit, budget, &resp, &lineNo)
			_ = f.Close()
			if budget.spent() || len(resp.Results) >= limit {
				resp.Truncated = true
				break
			}
		}
		if resp.Truncated {
			break
		}
	}

	writeJSONResponse(w, http.StatusOK, resp)
}

func scanNode(f *os.File, needle, runID, nodeID string, limit int, budget *searchBudget, resp *SearchResponse, lineNo *int) {
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		*lineNo++
		if budget.charge(int64(len(line)) + 1) {
			return
		}
		if !strings.Contains(strings.ToLower(line), needle) {
			continue
		}
		resp.Total++
		if len(resp.Results) < limit {
			resp.Results = append(resp.Results, SearchResult{
				RunID:   runID,
				NodeID:  nodeID,
				Line:    *lineNo,
				Content: line,
			})
		}
	}
	// safety: a line past the scanner's buffer ends the scan early, so the response must not claim completeness.
	if scanner.Err() != nil {
		resp.Truncated = true
	}
}

type searchBudget struct {
	ctx      context.Context
	deadline time.Time
	maxBytes int64
	bytes    int64
	lines    int
	stopped  bool
}

func (s *Server) newSearchBudget(ctx context.Context) *searchBudget {
	b := &searchBudget{ctx: ctx, maxBytes: s.limits.SearchMaxBytes}
	if s.limits.SearchTimeout > 0 {
		b.deadline = time.Now().Add(s.limits.SearchTimeout)
	}
	return b
}

func (b *searchBudget) spent() bool {
	if b.stopped {
		return true
	}
	if b.ctx.Err() != nil || (!b.deadline.IsZero() && time.Now().After(b.deadline)) {
		b.stopped = true
	}
	return b.stopped
}

// perf: the wall clock is read once per block of lines because a per-line read costs more than the scan itself.
func (b *searchBudget) charge(n int64) bool {
	if b.stopped {
		return true
	}
	b.bytes += n
	if b.maxBytes > 0 && b.bytes > b.maxBytes {
		b.stopped = true
		return true
	}
	b.lines++
	if b.lines%searchClockEvery != 0 {
		return false
	}
	return b.spent()
}

func writeJSONResponse(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeLogsErr(w http.ResponseWriter, status int, msg string) {
	writeJSONResponse(w, status, map[string]string{"error": msg})
}
