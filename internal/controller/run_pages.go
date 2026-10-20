package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type runPageCursor struct {
	Scope    string `json:"scope"`
	Snapshot int64  `json:"snapshot"`
	Through  int64  `json:"through"`
}

func newRunPageCursor(team string, query controllerwire.RunQuery) (runPageCursor, error) {
	raw := query.Cursor
	query.Cursor = ""
	scope, err := selectionFingerprint(team, query)
	if err != nil {
		return runPageCursor{}, err
	}
	cursor := runPageCursor{Scope: scope, Snapshot: math.MaxInt64, Through: math.MaxInt64}
	if raw != "" {
		body, err := base64.RawURLEncoding.DecodeString(raw)
		var decoded runPageCursor
		if err != nil || json.Unmarshal(body, &decoded) != nil || decoded.Scope != cursor.Scope || decoded.Snapshot < 1 || decoded.Through < query.AfterNumber || decoded.Through >= decoded.Snapshot {
			return cursor, invalidEngineBody("run cursor does not match this selection")
		}
		cursor = decoded
	}
	return cursor, nil
}

func (s *Server) handleRunPage(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	query, err := parseRunSelection(r)
	if err != nil {
		return err
	}
	tenant, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	cursor, err := newRunPageCursor(a.Target.Team, query)
	if err != nil {
		return err
	}
	page, err := s.readRunPage(r.Context(), tenant, a, query, &cursor)
	if err != nil {
		return engineLookupError(err)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, page)
	return nil
}

func (s *Server) readRunPage(ctx context.Context, tenant *store.Tenant, a capability.Authorized, query controllerwire.RunQuery, cursor *runPageCursor) (controllerwire.RunPage, error) {
	page := controllerwire.RunPage{Runs: []controllerwire.RunDetail{}}
	engine := s.engineController
	for through := cursor.Through; through > query.AfterNumber; {
		views, err := tenant.ListRunViews(ctx, query.AfterNumber, through, 100)
		if err != nil {
			return controllerwire.RunPage{}, err
		}
		if len(views) == 0 {
			break
		}
		if cursor.Snapshot == math.MaxInt64 {
			cursor.Snapshot = views[0].Snapshot.Run.Number
		}
		for _, view := range views {
			run := view.Snapshot.Run
			target := a.Target
			target.RunID = run.ID
			if err := s.operator.CheckBinding(ctx, a.Principal, a.Access.Binding, target); err != nil {
				return controllerwire.RunPage{}, err
			}
			through = run.Number - 1
			if !runSelectionMatches(view, query) {
				continue
			}
			if len(page.Runs) == query.Limit {
				body, err := json.Marshal(cursor)
				if err != nil {
					return controllerwire.RunPage{}, err
				}
				page.NextCursor, page.Truncated = base64.RawURLEncoding.EncodeToString(body), true
				return page, nil
			}
			facts, err := engine.runViewStatus(ctx, view)
			if err != nil {
				return controllerwire.RunPage{}, err
			}
			detail, err := runDetailResponse(target, facts)
			if err != nil {
				return controllerwire.RunPage{}, err
			}
			page.Runs = append(page.Runs, detail)
			cursor.Through = through
		}
	}
	return page, nil
}
