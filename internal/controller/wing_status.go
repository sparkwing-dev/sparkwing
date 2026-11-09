package controller

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (s *Server) handleWingStatus(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.reportingTenant(r, a)
	if err != nil {
		return err
	}
	runs, err := tenant.PipelineHealth(r.Context(), 94)
	if err != nil {
		return engineLookupError(err)
	}
	pending, err := tenant.PendingRunCount(r.Context())
	if err != nil {
		return engineLookupError(err)
	}
	snapshot, err := s.runnerInventory(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	base := strings.TrimRight(s.dashboardURL, "/")
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	doc := controllerwire.WingStatus{Source: "sparkwing", State: "ok", URL: base, UpdatedAt: now.Format(time.RFC3339), Items: []controllerwire.WingStatusItem{}}
	add := func(name, state, detail string) {
		doc.Items = append(doc.Items, controllerwire.WingStatusItem{Name: name, State: state, Detail: detail, URL: base, UpdatedAt: doc.UpdatedAt})
	}
	add("controller", "ok", "reachable")
	if s.localAdmission != nil {
		state, detail := "ok", "reachable"
		daemon, readErr := s.localAdmission(r.Context())
		if readErr != nil {
			state, detail = "fail", "admission daemon did not answer"
		} else if daemon.DaemonVersion == "" {
			state, detail = "unknown", "admission daemon is not running"
		}
		add("daemon", state, detail)
	}
	queueState := "ok"
	if pending > 0 {
		queueState = "warn"
	}
	add("queue", queueState, fmt.Sprintf("%d runs waiting to start", pending))
	online := 0
	for _, runner := range snapshot.Runners {
		if !runner.LastSeen.IsZero() && snapshot.Now.Sub(runner.LastSeen) <= agentOnlineWithin {
			online++
		}
	}
	runnerState := "ok"
	if online == 0 {
		runnerState = "unknown"
		if pending > 0 {
			runnerState = "warn"
		}
	}
	add("runners", runnerState, fmt.Sprintf("%d online runners", online))
	if len(runs) > 94 {
		runs = runs[:94]
		add("pipeline coverage", "warn", "showing the first 94 repository/pipeline results")
	}
	if len(runs) == 0 {
		add("pipelines", "unknown", "no default-branch runs recorded")
	}
	for _, run := range runs {
		doc.Items = append(doc.Items, pipelineStatusItem(run, base, now))
	}
	sort.Slice(doc.Items, func(i, j int) bool { return doc.Items[i].Name < doc.Items[j].Name })
	counts := map[string]int{}
	for _, item := range doc.Items {
		counts[item.State]++
	}
	switch {
	case counts["fail"] > 0:
		doc.State = "fail"
	case counts["warn"] > 0:
		doc.State = "warn"
	case counts["unknown"] > 0:
		doc.State = "unknown"
	}
	doc.Summary = fmt.Sprintf("%d failing, %d warning, %d ok checks", counts["fail"], counts["warn"], counts["ok"])
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, doc)
	return nil
}

func pipelineStatusItem(run store.PipelineHealthRun, base string, now time.Time) controllerwire.WingStatusItem {
	name := []rune(run.Repository + " / " + run.Pipeline)
	item := controllerwire.WingStatusItem{Name: string(name[:min(len(name), 200)]), State: "unknown", Detail: run.Status, URL: base + "/runs?run=" + url.QueryEscape(run.ID)}
	switch run.Status {
	case "success":
		item.State = "ok"
		if run.RetryOf != "" {
			item.State = "warn"
			item.Detail = "passed on retry"
		}
	case "failed":
		item.State = "fail"
	case "cancelled":
		item.State = "warn"
	case "running", "pending", "queued", "planning":
		start := run.StartedAt
		if start.IsZero() {
			start = run.CreatedAt
		}
		if now.Sub(start) > 30*time.Minute {
			item.State = "warn"
			item.Detail = run.Status + " for over 30 minutes"
		}
	}
	if run.FinishedAt != nil {
		item.UpdatedAt = run.FinishedAt.UTC().Format(time.RFC3339)
	}
	return item
}
