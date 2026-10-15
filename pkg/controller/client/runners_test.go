package client

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestRunnerReaderRejectsMissingAndConflictingDetails(t *testing.T) {
	for _, body := range []string{`{}`, `{"runners":[{}]}`, `{"agents":[]}`} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, body), nil })
		if _, err := c.ListRunners(t.Context()); err == nil {
			t.Fatalf("invalid runners accepted: %s", body)
		}
	}
	valid := controllerwire.RunnerDetail{Name: "desktop", Class: "self_hosted", Location: "local", Status: "idle"}
	for _, mutate := range []func(*controllerwire.RunnerDetail){
		func(r *controllerwire.RunnerDetail) { r.Name = "" },
		func(r *controllerwire.RunnerDetail) { r.Class = "" },
		func(r *controllerwire.RunnerDetail) { r.LastSeen = "yesterday" },
		func(r *controllerwire.RunnerDetail) { r.MaxConcurrent = -1 },
		func(r *controllerwire.RunnerDetail) { r.Capacity.Cores = -1 },
		func(r *controllerwire.RunnerDetail) { r.ActiveJobs = []controllerwire.RunnerJob{{JobID: "build"}} },
		func(r *controllerwire.RunnerDetail) {
			r.Headroom = &controllerwire.RunnerHeadroom{ObservedAt: "yesterday"}
		},
		func(r *controllerwire.RunnerDetail) {
			r.Headroom = &controllerwire.RunnerHeadroom{QueueDepth: -1, ObservedAt: "2026-10-14T01:02:03Z"}
		},
	} {
		invalid := valid
		mutate(&invalid)
		data, err := json.Marshal(controllerwire.Runners{Runners: []controllerwire.RunnerDetail{invalid}})
		if err != nil {
			t.Fatal(err)
		}
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, string(data)), nil })
		if _, err := c.ListRunners(t.Context()); err == nil {
			t.Fatalf("invalid detail accepted: %s", data)
		}
	}
	data, err := json.Marshal(controllerwire.Runners{Runners: []controllerwire.RunnerDetail{valid, valid}})
	if err != nil {
		t.Fatal(err)
	}
	c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, string(data)), nil })
	if _, err := c.ListRunners(t.Context()); err == nil {
		t.Fatal("duplicate runner identity accepted")
	}
}
