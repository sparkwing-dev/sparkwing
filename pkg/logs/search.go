package logs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
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
	Reason    string         `json:"reason,omitempty"`
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
	budget := s.newSearchBudget(r.Context())
	if s.archive != nil {
		lock := s.archive.lock(runFilter)
		lock.rw.RLock()
		defer lock.rw.RUnlock()
	}

	root, err := s.openRunsRoot()
	if err != nil {
		s.storeError(w, "open runs root", err)
		return
	}
	defer func() { _ = root.Close() }()
	entries, err := readRunDir(root, runFilter)
	if err != nil {
		if os.IsNotExist(err) {
			if s.archive != nil {
				s.searchArchive(w, r, runFilter, nodeFilter, strings.ToLower(q), limit, budget, &resp)
				return
			}
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}
		s.storeError(w, "read run dir", err)
		return
	}
	if !s.teamMayUse(r, readRunMeta(root, runFilter).Team) {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}

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
			resp.Reason = budget.reason()
			if resp.Reason == "" {
				resp.Reason = "result limit"
			}
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
				resp.Reason = budget.reason()
				if resp.Reason == "" {
					resp.Reason = "result limit"
				}
				break
			}
		}
		if resp.Truncated {
			break
		}
	}

	writeJSONResponse(w, http.StatusOK, resp)
}

func (s *Server) searchArchive(w http.ResponseWriter, r *http.Request, runID, nodeFilter, needle string, limit int, budget *searchBudget, resp *SearchResponse) {
	ctx := r.Context()
	if !budget.deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, budget.deadline)
		defer cancel()
	}
	idx, err := s.readRunIndex(ctx, runID)
	if errors.Is(err, teamblob.ErrNotFound) {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			resp.Truncated = true
			budget.spent()
			resp.Reason = budget.reason()
			if resp.Reason == "" {
				resp.Reason = "time budget"
			}
			writeJSONResponse(w, http.StatusOK, resp)
			return
		}
		s.storeError(w, "read run index", err)
		return
	}
	if !s.teamMayUse(r, idx.Team) {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	files := slices.Clone(idx.Files)
	slices.SortFunc(files, func(a, b archivedFile) int {
		aAttempt := strings.HasPrefix(a.Rel, ".attempts/")
		bAttempt := strings.HasPrefix(b.Rel, ".attempts/")
		if aAttempt != bAttempt {
			if aAttempt {
				return 1
			}
			return -1
		}
		return strings.Compare(a.Rel, b.Rel)
	})
	lineNos := map[string]int{}
	for _, file := range files {
		if !safeArchivedRel(file.Rel) || !strings.HasSuffix(file.Rel, ".log") {
			continue
		}
		nodeID := strings.TrimSuffix(filepath.Base(file.Rel), ".log")
		if parts := strings.Split(file.Rel, "/"); len(parts) == 3 && parts[0] == ".attempts" {
			nodeID = strings.TrimSuffix(parts[1], ".log")
		}
		if nodeFilter != "" && nodeID != nodeFilter {
			continue
		}
		if budget.spent() || len(resp.Results) >= limit {
			resp.Truncated = true
			resp.Reason = budget.reason()
			if resp.Reason == "" {
				resp.Reason = "result limit"
			}
			break
		}
		body, _, err := s.archive.store.Get(ctx, idx.Team, runObjectRel(runID, file.Rel))
		if errors.Is(err, teamblob.ErrNotFound) {
			continue
		}
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				resp.Truncated = true
				budget.spent()
				resp.Reason = budget.reason()
				if resp.Reason == "" {
					resp.Reason = "time budget"
				}
				writeJSONResponse(w, http.StatusOK, resp)
				return
			}
			s.storeError(w, "read archived log", err)
			return
		}
		lineNo := lineNos[nodeID]
		scanNode(body, needle, runID, nodeID, limit, budget, resp, &lineNo)
		lineNos[nodeID] = lineNo
		_ = body.Close()
		if resp.Truncated {
			break
		}
	}
	writeJSONResponse(w, http.StatusOK, resp)
}

func scanNode(f io.Reader, needle, runID, nodeID string, limit int, budget *searchBudget, resp *SearchResponse, lineNo *int) {
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		*lineNo++
		if budget.charge(int64(len(line)) + 1) {
			resp.Truncated = true
			resp.Reason = budget.reason()
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
			if len(resp.Results) >= limit {
				resp.Truncated = true
				resp.Reason = "result limit"
				return
			}
		}
	}
	// safety: a line past the scanner's buffer ends the scan early, so the response must not claim completeness.
	if scanner.Err() != nil {
		resp.Truncated = true
		budget.spent()
		resp.Reason = budget.reason()
		if resp.Reason == "" {
			resp.Reason = "log read error"
		}
	}
}

type searchBudget struct {
	ctx        context.Context
	deadline   time.Time
	maxBytes   int64
	bytes      int64
	lines      int
	stopped    bool
	stopReason string
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
	if b.ctx.Err() != nil {
		b.stopReason = "request canceled"
		b.stopped = true
	} else if !b.deadline.IsZero() && time.Now().After(b.deadline) {
		b.stopReason = "time budget"
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
		b.stopReason = "byte budget"
		b.stopped = true
		return true
	}
	b.lines++
	if b.lines%searchClockEvery != 0 {
		return false
	}
	return b.spent()
}

func (b *searchBudget) reason() string { return b.stopReason }

func writeJSONResponse(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeLogsErr(w http.ResponseWriter, status int, msg string) {
	writeJSONResponse(w, status, map[string]string{"error": msg})
}
