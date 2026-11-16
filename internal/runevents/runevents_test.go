package runevents

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func finishedFacts() Facts {
	created := time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)
	started := created.Add(time.Minute)
	finished := started.Add(4 * time.Minute)
	return Facts{
		Source: Source("store1", "acme"), StateSeq: 2, Time: finished,
		RunID: "run_1", RunNumber: 412, Pipeline: "test", Status: "failed", Reason: "job_failed",
		RepositoryKey: "github.com/acme/app", RepositoryDisplay: "acme/app", GitBranch: "main", GitSHA: "4f1c",
		Trigger: "push", CreatedAt: created, StartedAt: &started, FinishedAt: &finished,
		RootRunID: "run_0", URL: "https://dash.example/runs/run_1",
	}
}

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBuildFinishedCarriesTheEnvelopeAndAllowlistedData(t *testing.T) {
	body, err := Build(EventFinished, finishedFacts())
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, body)
	want := map[string]any{
		"specversion": "1.0", "id": "run_1/2", "source": "urn:sparkwing:store1:team:acme",
		"type": TypeFinished, "subject": "runs/run_1", "time": "2026-11-13T18:05:00.000Z",
		"datacontenttype": "application/json", "sparkwingstateseq": float64(2),
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	d := got["data"].(map[string]any)
	keys := []string{}
	for k := range d {
		keys = append(keys, k)
	}
	allowed := map[string]bool{"run_id": true, "run_number": true, "pipeline": true, "status": true, "reason": true, "repository": true, "git": true, "trigger": true, "created_at": true, "started_at": true, "finished_at": true, "lineage": true, "url": true}
	for _, k := range keys {
		if !allowed[k] {
			t.Errorf("data carries %q outside the allowlist", k)
		}
	}
	if len(keys) != len(allowed) {
		t.Errorf("data keys = %v, want every allowlisted key for a full run", keys)
	}
	if d["status"] != "failed" || d["reason"] != "job_failed" || d["started_at"] != "2026-11-13T18:01:00.000Z" {
		t.Errorf("data = %v", d)
	}
	l := d["lineage"].(map[string]any)
	if l["root_run_id"] != "run_0" || l["parent_run_id"] != nil || l["retry_of"] != nil {
		t.Errorf("lineage = %v", l)
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	a, _ := Build(EventFinished, finishedFacts())
	b, _ := Build(EventFinished, finishedFacts())
	if !bytes.Equal(a, b) {
		t.Fatalf("bodies differ:\n%s\n%s", a, b)
	}
}

func TestBuildOmitsMissingFactsAndNullsUnstartedRuns(t *testing.T) {
	f := finishedFacts()
	f.StartedAt, f.URL, f.Reason, f.RepositoryKey, f.GitBranch, f.GitSHA, f.Trigger = nil, "", "", "", "", "", ""
	f.Status = "cancelled"
	body, err := Build(EventFinished, f)
	if err != nil {
		t.Fatal(err)
	}
	d := decode(t, body)["data"].(map[string]any)
	if v, ok := d["started_at"]; !ok || v != nil {
		t.Errorf("started_at = %v, %v; want explicit null", v, ok)
	}
	for _, k := range []string{"url", "reason", "repository", "git", "trigger"} {
		if _, ok := d[k]; ok {
			t.Errorf("data carries %q though the fact is missing", k)
		}
	}
}

func TestBuildStartedOmitsFinishedAt(t *testing.T) {
	f := finishedFacts()
	f.Status, f.FinishedAt, f.StateSeq = "running", nil, 1
	body, err := Build(EventStarted, f)
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, body)
	if got["type"] != TypeStarted || got["id"] != "run_1/1" {
		t.Fatalf("envelope = %v", got)
	}
	if _, ok := got["data"].(map[string]any)["finished_at"]; ok {
		t.Fatal("run.started carries finished_at")
	}
}

func TestBuildRefusesIncompleteOrUnknownEvents(t *testing.T) {
	f := finishedFacts()
	if _, err := Build("run.queued", f); err == nil {
		t.Error("unknown event accepted")
	}
	f.Status = "running"
	if _, err := Build(EventFinished, f); err == nil {
		t.Error("run.finished with a live status accepted")
	}
	f = finishedFacts()
	f.Source = ""
	if _, err := Build(EventFinished, f); err == nil {
		t.Error("event without a source accepted")
	}
}

func TestFilterValidate(t *testing.T) {
	cases := []struct {
		name string
		f    Filter
		ok   bool
	}{
		{"both types", Filter{EventTypes: []string{EventStarted, EventFinished}}, true},
		{"finished failures", Filter{EventTypes: []string{EventFinished}, Statuses: []string{"failed"}, Pipelines: []string{"test"}, Repositories: []string{"gh:1"}}, true},
		{"no types", Filter{}, false},
		{"unknown type", Filter{EventTypes: []string{"run.succeeded"}}, false},
		{"unknown status", Filter{EventTypes: []string{EventFinished}, Statuses: []string{"running"}}, false},
		{"statuses without finished", Filter{EventTypes: []string{EventStarted}, Statuses: []string{"failed"}}, false},
		{"repeat", Filter{EventTypes: []string{EventFinished, EventFinished}}, false},
		{"empty pipeline", Filter{EventTypes: []string{EventFinished}, Pipelines: []string{""}}, false},
		{"padded pipeline", Filter{EventTypes: []string{EventFinished}, Pipelines: []string{" test"}}, false},
		{"too many", Filter{EventTypes: []string{EventFinished}, Pipelines: strings.Fields(strings.Repeat("p ", 51))}, false},
	}
	for _, c := range cases {
		if err := c.f.Validate(); (err == nil) != c.ok {
			t.Errorf("%s: Validate() = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestFilterMatches(t *testing.T) {
	all := Filter{EventTypes: []string{EventStarted, EventFinished}}
	failures := Filter{EventTypes: []string{EventStarted, EventFinished}, Statuses: []string{"failed"}, Pipelines: []string{"test"}, Repositories: []string{"gh:1"}}
	cases := []struct {
		f                        Filter
		event, status, pipe, rep string
		want                     bool
	}{
		{all, EventStarted, "running", "any", "any", true},
		{all, EventFinished, "success", "any", "", true},
		{failures, EventFinished, "failed", "test", "gh:1", true},
		{failures, EventFinished, "success", "test", "gh:1", false},
		{failures, EventStarted, "running", "test", "gh:1", true},
		{failures, EventFinished, "failed", "build", "gh:1", false},
		{failures, EventFinished, "failed", "test", "gh:2", false},
		{Filter{EventTypes: []string{EventFinished}}, EventStarted, "running", "test", "gh:1", false},
	}
	for i, c := range cases {
		if got := c.f.Matches(c.event, c.status, c.pipe, c.rep); got != c.want {
			t.Errorf("case %d: Matches = %v, want %v", i, got, c.want)
		}
	}
}
