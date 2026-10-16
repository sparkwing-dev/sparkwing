package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/api"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const logPageRecords = 500

type logPageCursor struct {
	Scope   string           `json:"scope"`
	Since   time.Time        `json:"since"`
	Lines   map[string]int64 `json:"lines"`
	Events  map[string]int64 `json:"events"`
	Matches map[string]int64 `json:"matches,omitempty"`
}

func parseLogPageQuery(r *http.Request, job string) (controllerwire.LogQuery, error) {
	values := r.URL.Query()
	q := controllerwire.LogQuery{Job: job, Grep: values.Get("grep"), Lines: values.Get("lines"), Cursor: values.Get("cursor")}
	if q.Job == "" {
		q.Job = values.Get("job")
	}
	for key, target := range map[string]**int{"head": &q.Head, "tail": &q.Tail} {
		if values.Has(key) {
			v, err := strconv.Atoi(values.Get(key))
			if err != nil {
				return q, invalidEngineBody(key + " must be a nonnegative integer")
			}
			*target = &v
		}
	}
	for key, target := range map[string]*bool{"tree": &q.Tree, "events_only": &q.EventsOnly, "no_events": &q.NoEvents} {
		if values.Has(key) {
			v, err := strconv.ParseBool(values.Get(key))
			if err != nil {
				return q, invalidEngineBody(key + " must be a boolean")
			}
			*target = v
		}
	}
	if values.Has("since") {
		var err error
		q.Since, err = time.ParseDuration(values.Get("since"))
		if err != nil {
			return q, invalidEngineBody("since must be a duration")
		}
	}
	if err := q.Validate(); err != nil {
		return q, invalidEngineBody(err.Error())
	}
	return q, nil
}

func newLogPageCursor(raw, run, team string, q controllerwire.LogQuery) (logPageCursor, error) {
	q.Cursor = ""
	encoded, err := json.Marshal(struct {
		Run, Team string
		Query     controllerwire.LogQuery
	}{run, team, q})
	if err != nil {
		return logPageCursor{}, err
	}
	digest := sha256.Sum256(encoded)
	scope := hex.EncodeToString(digest[:])
	cursor := logPageCursor{Scope: scope, Lines: map[string]int64{}, Events: map[string]int64{}}
	if raw != "" {
		body, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(body, &cursor) != nil || cursor.Scope != scope || cursor.Lines == nil || cursor.Events == nil {
			return cursor, invalidEngineBody("log cursor does not match this selection")
		}
		for _, offsets := range []map[string]int64{cursor.Lines, cursor.Events} {
			for _, offset := range offsets {
				if offset < 0 {
					return cursor, invalidEngineBody("log cursor contains a negative offset")
				}
			}
		}
	} else if q.Since > 0 {
		cursor.Since = time.Now().Add(-q.Since)
	}
	return cursor, nil
}

func (s *Server) pagedLogRead(fallback func(http.ResponseWriter, *http.Request, capability.Authorized) error) func(http.ResponseWriter, *http.Request, capability.Authorized) error {
	return func(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
		if r.Header.Get("Accept") != "application/json" {
			return fallback(w, r, a)
		}
		q, err := parseLogPageQuery(r, a.Target.NodeID)
		if err != nil {
			return err
		}
		tenant, err := s.reportingTenant(r, a)
		if err != nil {
			return engineLookupError(err)
		}
		cursor, err := newLogPageCursor(q.Cursor, a.Target.RunID, a.Target.Team, q)
		if err != nil {
			return err
		}
		reader := segmentLogs{records: tenant, actor: engineActor(a), objs: s.logObjects()}
		page, err := s.readLogPage(r.Context(), tenant, reader, a, q, &cursor)
		if err != nil {
			return engineLookupError(err)
		}
		body, err := json.Marshal(page)
		if err != nil {
			return err
		}
		s.meterResponse(w, r, egress.ClassLog, egress.SlotDownload, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(body)
		}), egress.TeamPrincipal(a.Target.Team, a.Principal.Capability.Subject.ID), tenant.Team())
		return nil
	}
}

func (s *Server) readLogPage(ctx context.Context, tenant *store.Tenant, reader segmentLogs, a capability.Authorized, q controllerwire.LogQuery, cursor *logPageCursor) (controllerwire.LogPage, error) {
	page := controllerwire.LogPage{Records: []controllerwire.LogRecord{}, Complete: true}
	var pattern *regexp.Regexp
	if q.Grep != "" {
		var err error
		pattern, err = regexp.Compile(q.Grep)
		if err != nil {
			return page, invalidEngineBody("invalid log search expression")
		}
	}
	runs, seen := []string{a.Target.RunID}, map[string]bool{a.Target.RunID: true}
	for at := 0; at < len(runs); at++ {
		run := runs[at]
		if run != a.Target.RunID {
			target := a.Target
			target.RunID = run
			if err := s.store.CheckBinding(ctx, a.Principal, a.Access.Binding, target); err != nil {
				return page, err
			}
		}
		view, err := tenant.ReadRunView(ctx, run)
		if err != nil {
			return page, err
		}
		page.Complete = page.Complete && view.Snapshot.Run.FinishedAt != nil
		if q.Tree {
			filter := store.RunFilter{ParentRunID: run, Limit: 100}
			for {
				children, err := tenant.ListRuns(ctx, filter)
				if err != nil {
					return page, err
				}
				for _, child := range children {
					if !seen[child.ID] {
						seen[child.ID] = true
						runs = append(runs, child.ID)
					}
				}
				if len(children) < filter.Limit {
					break
				}
				last := children[len(children)-1]
				filter.AfterID, filter.AfterStartedAt = last.ID, last.StartedAt.UnixNano()
				if last.StartedAt.IsZero() {
					filter.AfterStartedAt = 0
				}
			}
		}
		if !q.EventsOnly {
			for _, subject := range view.Logs {
				if q.Job != "" && (subject.Attempt == nil || subject.Attempt.NodeID != q.Job) {
					continue
				}
				if err := appendSubjectPage(ctx, reader, subject, q, pattern, cursor, &page); err != nil {
					return page, err
				}
				if page.Truncated {
					break
				}
			}
		}
		if !page.Truncated && !q.NoEvents {
			if err := appendEventPage(ctx, tenant, run, q, pattern, cursor, &page); err != nil {
				return page, err
			}
		}
		if page.Truncated {
			break
		}
	}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return page, err
	}
	page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	page.Complete = page.Complete && !page.Truncated
	return page, page.Validate(q.Cursor)
}

func appendSubjectPage(ctx context.Context, reader segmentLogs, subject blob.LogSubject, q controllerwire.LogQuery, pattern *regexp.Regexp, cursor *logPageCursor, page *controllerwire.LogPage) error {
	return appendSubjectPageLimit(ctx, reader, subject, q, 0, pattern, cursor, page)
}

func appendSubjectPageLimit(ctx context.Context, reader segmentLogs, subject blob.LogSubject, q controllerwire.LogQuery, maxMatches int, pattern *regexp.Regexp, cursor *logPageCursor, page *controllerwire.LogPage) error {
	var output bytes.Buffer
	result, err := reader.read(ctx, subject, 0, &output)
	if err != nil {
		return err
	}
	if result.IncompleteReason != "" {
		return &apierr.Error{Code: apierr.IntegrityFailure, Message: "logs incomplete: " + result.IncompleteReason}
	}
	page.Complete = page.Complete && result.Complete
	key, err := json.Marshal(subject)
	if err != nil {
		return err
	}
	_, observed := cursor.Lines[string(key)]
	if !observed {
		cursor.Lines[string(key)] = 0
	}
	lines := []string{}
	if output.Len() > 0 {
		lines = strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	}
	first, last := 1, len(lines)
	if q.Head != nil {
		last = min(last, *q.Head)
	}
	if q.Tail != nil && !observed {
		first = max(first, len(lines)-*q.Tail+1)
	}
	if q.Lines != "" {
		var start, end int
		fmt.Sscanf(q.Lines, "%d:%d", &start, &end)
		first, last = max(first, start), min(last, end)
	}
	for i, line := range lines {
		number := int64(i + 1)
		if number <= cursor.Lines[string(key)] {
			continue
		}
		record := controllerwire.LogRecord{RunID: subject.RunID, Subject: &subject, LineNo: number, Line: line}
		var frame nodeproto.FrameLog
		if json.Unmarshal([]byte(line), &frame) == nil && frame.TS != "" {
			at, err := time.Parse(time.RFC3339Nano, frame.TS)
			if err != nil {
				return &apierr.Error{Code: apierr.IntegrityFailure, Message: "log record has an invalid timestamp"}
			}
			record.Time = &at
		}
		selected := i+1 >= first && i+1 <= last && (pattern == nil || pattern.MatchString(line)) && (record.Time == nil || !record.Time.Before(cursor.Since)) && (maxMatches == 0 || cursor.Matches[string(key)] < int64(maxMatches))
		if selected && len(page.Records) == logPageRecords {
			page.Truncated = true
			return nil
		}
		cursor.Lines[string(key)] = number
		if selected {
			page.Records = append(page.Records, record)
			if maxMatches > 0 {
				cursor.Matches[string(key)]++
			}
		}
	}
	return nil
}

func appendEventPage(ctx context.Context, tenant *store.Tenant, run string, q controllerwire.LogQuery, pattern *regexp.Regexp, cursor *logPageCursor, page *controllerwire.LogPage) error {
	for {
		events, err := tenant.ListEventsAfter(ctx, run, cursor.Events[run], logPageRecords)
		if err != nil {
			return err
		}
		for _, event := range api.PublicEvents(events) {
			selected := (q.Job == "" || event.NodeID == q.Job) && !event.TS.Before(cursor.Since) && (pattern == nil || pattern.MatchString(event.Kind+" "+string(event.Payload)))
			if selected && len(page.Records) == logPageRecords {
				page.Truncated = true
				return nil
			}
			cursor.Events[run] = event.Seq
			if selected {
				page.Records = append(page.Records, controllerwire.LogRecord{RunID: run, Event: event.Kind, JobID: event.NodeID, Time: &event.TS, LineNo: event.Seq, Line: string(event.Payload)})
			}
		}
		if len(events) < logPageRecords {
			return nil
		}
	}
}
