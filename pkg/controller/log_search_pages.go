package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type logSearchCursor struct {
	Scope    string        `json:"scope"`
	Snapshot int64         `json:"snapshot"`
	Through  int64         `json:"through"`
	Logs     logPageCursor `json:"logs"`
}

func newLogSearchCursor(team string, q controllerwire.LogSearchQuery) (logSearchCursor, error) {
	raw := q.Runs.Cursor
	q.Runs.Cursor = ""
	scope, err := selectionFingerprint(team, q)
	if err != nil {
		return logSearchCursor{}, err
	}
	cursor := logSearchCursor{Scope: scope, Snapshot: math.MaxInt64, Through: math.MaxInt64}
	if raw != "" {
		body, err := base64.RawURLEncoding.DecodeString(raw)
		var decoded logSearchCursor
		if err != nil || json.Unmarshal(body, &decoded) != nil || decoded.Scope != cursor.Scope || decoded.Through < 0 || decoded.Through > decoded.Snapshot || decoded.Snapshot < 1 || decoded.Logs.Lines == nil {
			return cursor, invalidEngineBody("log search cursor does not match this selection")
		}
		cursor = decoded
		for _, offsets := range []map[string]int64{cursor.Logs.Lines, cursor.Logs.Events, cursor.Logs.Matches} {
			for _, offset := range offsets {
				if offset < 0 {
					return cursor, invalidEngineBody("log search cursor contains a negative offset")
				}
			}
		}
	}
	cursor.prepareLogs()
	return cursor, nil
}

func (c *logSearchCursor) prepareLogs() {
	if c.Logs.Lines == nil {
		c.Logs = logPageCursor{Lines: map[string]int64{}, Events: map[string]int64{}, Matches: map[string]int64{}}
	}
	if c.Logs.Matches == nil {
		c.Logs.Matches = map[string]int64{}
	}
}

func (s *Server) pagedLogSearch(fallback func(http.ResponseWriter, *http.Request, capability.Authorized) error) func(http.ResponseWriter, *http.Request, capability.Authorized) error {
	return func(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
		if r.Header.Get("Accept") != "application/json" {
			return fallback(w, r, a)
		}
		runs, err := parseRunSelection(r)
		if err != nil {
			return err
		}
		q := controllerwire.LogSearchQuery{Runs: runs, Pattern: r.URL.Query().Get("grep"), Job: r.URL.Query().Get("job")}
		if r.URL.Query().Has("max_matches") {
			q.MaxMatches, err = strconv.Atoi(r.URL.Query().Get("max_matches"))
			if err != nil || q.MaxMatches < 0 {
				return invalidEngineBody("max_matches must be a nonnegative integer")
			}
		}
		pattern, err := regexp.Compile(q.Pattern)
		if err != nil || q.Pattern == "" {
			return invalidEngineBody("grep must be a nonempty valid regular expression")
		}
		tenant, err := s.reportingTenant(r, a)
		if err != nil {
			return engineLookupError(err)
		}
		cursor, err := newLogSearchCursor(a.Target.Team, q)
		if err != nil {
			return err
		}
		page, err := s.readLogSearchPage(r.Context(), tenant, a, q, pattern, &cursor)
		if err != nil {
			return engineLookupError(err)
		}
		s.meterResponse(w, r, egress.ClassLog, egress.SlotDownload, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, page)
		}), egress.TeamPrincipal(a.Target.Team, a.Principal.Capability.Subject.ID), tenant.Team())
		return nil
	}
}

func (s *Server) readLogSearchPage(ctx context.Context, tenant *store.Tenant, a capability.Authorized, q controllerwire.LogSearchQuery, pattern *regexp.Regexp, cursor *logSearchCursor) (controllerwire.LogSearchPage, error) {
	page := controllerwire.LogPage{Records: []controllerwire.LogRecord{}}
	reader := segmentLogs{records: tenant, actor: engineActor(a), objs: s.logObjects()}
	selected := 0
	for cursor.Through > q.Runs.AfterNumber && selected < q.Runs.Limit && !page.Truncated {
		views, err := tenant.ListRunViews(ctx, q.Runs.AfterNumber, cursor.Through, min(q.Runs.Limit-selected, 100))
		if err != nil {
			return controllerwire.LogSearchPage{}, err
		}
		if len(views) == 0 {
			cursor.Through = q.Runs.AfterNumber
			break
		}
		if cursor.Snapshot == math.MaxInt64 {
			cursor.Snapshot = views[0].Snapshot.Run.Number
		}
		for _, view := range views {
			run := view.Snapshot.Run
			target := a.Target
			target.RunID = run.ID
			if err := s.store.CheckBinding(ctx, a.Principal, a.Access.Binding, target); err != nil {
				return controllerwire.LogSearchPage{}, err
			}
			cursor.Through = run.Number
			if runSelectionMatches(view, q.Runs) {
				for _, subject := range view.Logs {
					if q.Job != "" && (subject.Attempt == nil || subject.Attempt.NodeID != q.Job) {
						continue
					}
					if err := appendSubjectPageLimit(ctx, reader, subject, controllerwire.LogQuery{}, q.MaxMatches, pattern, &cursor.Logs, &page); err != nil {
						return controllerwire.LogSearchPage{}, err
					}
					if page.Truncated {
						break
					}
				}
				selected++
			}
			if page.Truncated {
				break
			}
			cursor.Through--
			cursor.Logs = logPageCursor{}
			cursor.prepareLogs()
		}
	}
	result := controllerwire.LogSearchPage{Matches: page.Records, Truncated: page.Truncated}
	if !result.Truncated && cursor.Through > q.Runs.AfterNumber {
		views, err := tenant.ListRunViews(ctx, q.Runs.AfterNumber, cursor.Through, 1)
		if err != nil {
			return result, err
		}
		result.Truncated = len(views) != 0
	}
	if result.Truncated {
		body, err := json.Marshal(cursor)
		if err != nil {
			return result, err
		}
		result.NextCursor = base64.RawURLEncoding.EncodeToString(body)
	}
	return result, result.Validate(q.Runs.Cursor)
}
