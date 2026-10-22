package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func selectionFingerprint(team string, query any) (string, error) {
	body, err := json.Marshal(struct {
		Team  string
		Query any
	}{team, query})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

func parseRunSelection(r *http.Request) (controllerwire.RunQuery, error) {
	v := r.URL.Query()
	q := controllerwire.RunQuery{Limit: 50, Cursor: v.Get("cursor"), ParentRunID: v.Get("parent_run_id"), Pipeline: v.Get("pipeline"), Status: v.Get("status"), Branches: v["branch"], SHA: v.Get("sha"), Repos: v["repo"], Error: v.Get("error"), Search: v.Get("search")}
	if v.Has("limit") {
		n, err := strconv.Atoi(v.Get("limit"))
		if err != nil || n < 1 {
			return q, invalidEngineBody("limit must be a positive integer")
		}
		q.Limit = min(n, store.MaxRunListLimit)
	}
	if v.Has("root_only") {
		b, err := strconv.ParseBool(v.Get("root_only"))
		if err != nil {
			return q, invalidEngineBody("root_only must be a boolean")
		}
		q.RootOnly = b
	}
	if v.Has("after_number") {
		n, err := strconv.ParseInt(v.Get("after_number"), 10, 64)
		if err != nil || n < 0 {
			return q, invalidEngineBody("after_number must be a nonnegative integer")
		}
		q.AfterNumber = n
	}
	for key, target := range map[string]*time.Time{"started_after": &q.StartedAfter, "started_before": &q.StartedBefore, "finished_after": &q.FinishedAfter, "finished_before": &q.FinishedBefore} {
		if v.Has(key) {
			at, err := time.Parse(time.RFC3339Nano, v.Get(key))
			if err != nil {
				return q, invalidEngineBody(key + " must be an RFC3339 time")
			}
			*target = at
		}
	}
	if !q.StartedBefore.IsZero() && q.StartedAfter.After(q.StartedBefore) || !q.FinishedBefore.IsZero() && q.FinishedAfter.After(q.FinishedBefore) {
		return q, invalidEngineBody("run query has an inverted date range")
	}
	return q, nil
}

func runSelectionMatches(view store.RunView, q controllerwire.RunQuery) bool {
	run := view.Snapshot.Run
	display := run.Display()
	if run.Number <= q.AfterNumber || q.ParentRunID != "" && run.Lineage.ParentRunID != q.ParentRunID || q.ParentRunID == "" && q.RootOnly && run.Lineage.ParentRunID != "" ||
		!selectionValues([]string{q.Pipeline}, false, run.Pipeline) || !selectionValues([]string{q.Status}, false, string(run.State)) ||
		!selectionValues(q.Branches, false, display.Git.Ref) || !selectionValues([]string{q.SHA}, true, display.Git.SHA) ||
		!selectionValues(q.Repos, false, display.Git.Repository.Display, string(display.Git.Repository.Key)) ||
		!selectionTime(run.StartedAt, q.StartedAfter, q.StartedBefore) || !selectionTime(run.FinishedAt, q.FinishedAfter, q.FinishedBefore) {
		return false
	}
	failures := append([]string{}, view.Failures...)
	for _, node := range view.Snapshot.Nodes {
		failures = append(failures, node.StatusDetail)
	}
	for _, attempt := range view.Attempts {
		failures = append(failures, attempt.FailureReason)
	}
	failure := strings.ToLower(strings.Join(failures, " "))
	if !strings.Contains(failure, strings.ToLower(q.Error)) {
		return false
	}
	text := strings.ToLower(strings.Join([]string{run.ID, strconv.FormatInt(run.Number, 10), run.Pipeline, display.Git.Repository.Display, string(display.Git.Repository.Key), display.Git.Ref, display.Git.SHA, failure}, " "))
	include, exclude := store.ParseRunSearch(q.Search)
	for _, term := range include {
		if !strings.Contains(text, strings.ToLower(term)) {
			return false
		}
	}
	for _, term := range exclude {
		if strings.Contains(text, strings.ToLower(term)) {
			return false
		}
	}
	return true
}

func selectionValues(expressions []string, prefix bool, values ...string) bool {
	positive, matched := false, false
	for _, expression := range expressions {
		if expression == "" {
			continue
		}
		negated := strings.HasPrefix(expression, "!")
		expression = strings.TrimPrefix(expression, "!")
		match := false
		for _, value := range values {
			if prefix {
				match = match || strings.HasPrefix(strings.ToLower(value), strings.ToLower(expression))
			} else {
				match = match || value == expression
			}
		}
		if negated && match {
			return false
		}
		if !negated {
			positive, matched = true, matched || match
		}
	}
	return !positive || matched
}

func selectionTime(value *time.Time, after, before time.Time) bool {
	if after.IsZero() && before.IsZero() {
		return true
	}
	return value != nil && (after.IsZero() || !value.Before(after)) && (before.IsZero() || !value.After(before))
}
