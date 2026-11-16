// Package runevents defines the CloudEvents 1.0 body Sparkwing sends for run
// lifecycle changes and the filter a subscription uses to select them.
//
// The package has no access to run requests, environment, logs or errors: a
// body is built only from the allowlisted values in Facts.
package runevents

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Event names a subscription filters on.
const (
	EventStarted  = "run.started"
	EventFinished = "run.finished"
)

// CloudEvents types. A breaking change to the data gets a new type.
const (
	TypeStarted  = "dev.sparkwing.run.started.v1"
	TypeFinished = "dev.sparkwing.run.finished.v1"
	// ContentType is the structured-mode media type of a body Build returns.
	ContentType = "application/cloudevents+json"
)

var finishedStatuses = []string{"success", "failed", "cancelled", "dry_run"}

// Facts holds every value an event may carry; nothing else reaches the body.
type Facts struct {
	// Source is the value Source returns for this store and team.
	Source string
	// StateSeq is the run's state sequence after the transition.
	StateSeq int64
	// Time is when the transition committed.
	Time              time.Time
	RunID             string
	RunNumber         int64
	Pipeline          string
	Status            string
	Reason            string
	RepositoryKey     string
	RepositoryDisplay string
	GitBranch         string
	GitSHA            string
	Trigger           string
	CreatedAt         time.Time
	StartedAt         *time.Time
	FinishedAt        *time.Time
	RootRunID         string
	ParentRunID       string
	RetryOf           string
	// URL is the run's dashboard page; empty when no dashboard URL is configured.
	URL string
}

type envelope struct {
	SpecVersion     string `json:"specversion"`
	ID              string `json:"id"`
	Source          string `json:"source"`
	Type            string `json:"type"`
	Subject         string `json:"subject"`
	Time            string `json:"time"`
	DataContentType string `json:"datacontenttype"`
	StateSeq        int64  `json:"sparkwingstateseq"`
	Data            data   `json:"data"`
}

type data struct {
	RunID      string      `json:"run_id"`
	RunNumber  int64       `json:"run_number"`
	Pipeline   string      `json:"pipeline"`
	Status     string      `json:"status"`
	Reason     string      `json:"reason,omitempty"`
	Repository *repository `json:"repository,omitempty"`
	Git        *git        `json:"git,omitempty"`
	Trigger    string      `json:"trigger,omitempty"`
	CreatedAt  string      `json:"created_at"`
	StartedAt  *string     `json:"started_at"`
	FinishedAt *string     `json:"finished_at,omitempty"`
	Lineage    lineage     `json:"lineage"`
	URL        string      `json:"url,omitempty"`
}

type repository struct {
	Key     string `json:"key"`
	Display string `json:"display,omitempty"`
}

type git struct {
	Branch string `json:"branch,omitempty"`
	SHA    string `json:"sha,omitempty"`
}

type lineage struct {
	RootRunID   *string `json:"root_run_id"`
	ParentRunID *string `json:"parent_run_id"`
	RetryOf     *string `json:"retry_of"`
}

// Build returns the structured-mode CloudEvent body for event, which is
// EventStarted or EventFinished. Identical inputs give identical bytes, so a
// retried delivery carries the same body.
func Build(event string, f Facts) ([]byte, error) {
	var eventType string
	switch event {
	case EventStarted:
		eventType = TypeStarted
	case EventFinished:
		eventType = TypeFinished
		if !slices.Contains(finishedStatuses, f.Status) {
			return nil, fmt.Errorf("run.finished needs a terminal status, got %q", f.Status)
		}
	default:
		return nil, fmt.Errorf("unknown run event %q", event)
	}
	if f.Source == "" || f.RunID == "" || f.StateSeq <= 0 || f.Time.IsZero() || f.CreatedAt.IsZero() {
		return nil, errors.New("run event needs a source, run, state sequence, time and creation time")
	}
	d := data{
		RunID: f.RunID, RunNumber: f.RunNumber, Pipeline: f.Pipeline, Status: f.Status, Reason: f.Reason,
		Trigger: f.Trigger, CreatedAt: stamp(f.CreatedAt), StartedAt: optionalStamp(f.StartedAt),
		FinishedAt: optionalStamp(f.FinishedAt), URL: f.URL,
		Lineage: lineage{RootRunID: optional(f.RootRunID), ParentRunID: optional(f.ParentRunID), RetryOf: optional(f.RetryOf)},
	}
	if f.RepositoryKey != "" {
		d.Repository = &repository{Key: f.RepositoryKey, Display: f.RepositoryDisplay}
	}
	if f.GitBranch != "" || f.GitSHA != "" {
		d.Git = &git{Branch: f.GitBranch, SHA: f.GitSHA}
	}
	return json.Marshal(envelope{
		SpecVersion: "1.0", ID: ID(f.RunID, f.StateSeq), Source: f.Source, Type: eventType,
		Subject: "runs/" + f.RunID, Time: stamp(f.Time), DataContentType: "application/json",
		StateSeq: f.StateSeq, Data: d,
	})
}

// ID is the event ID: the same for every attempt and redelivery of one state change.
func ID(runID string, stateSeq int64) string {
	return runID + "/" + strconv.FormatInt(stateSeq, 10)
}

// Source is the CloudEvents source for a team in one store. Receivers
// deduplicate on source and ID together.
func Source(storeID, team string) string {
	return "urn:sparkwing:" + storeID + ":team:" + team
}

func stamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func optionalStamp(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := stamp(*t)
	return &s
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Filter selects the events a subscription receives. An empty list matches
// every value.
type Filter struct {
	// EventTypes holds EventStarted and/or EventFinished and is never empty.
	EventTypes []string `json:"event_types"`
	// Statuses limits run.finished to these terminal statuses.
	Statuses []string `json:"statuses"`
	// Pipelines are exact pipeline names.
	Pipelines []string `json:"pipelines"`
	// Repositories are exact repository keys, as runs report them.
	Repositories []string `json:"repositories"`
}

const maxFilterValues = 50

// Validate refuses unknown event types or statuses, empty or repeated values,
// and statuses on a filter that does not select run.finished.
func (f Filter) Validate() error {
	if len(f.EventTypes) == 0 {
		return errors.New("event_types needs run.started, run.finished or both")
	}
	for name, values := range map[string][]string{"event_types": f.EventTypes, "statuses": f.Statuses, "pipelines": f.Pipelines, "repositories": f.Repositories} {
		if len(values) > maxFilterValues {
			return fmt.Errorf("%s holds at most %d values", name, maxFilterValues)
		}
		for i, v := range values {
			if strings.TrimSpace(v) != v || v == "" || len(v) > 256 || strings.ContainsAny(v, "\x00\r\n") {
				return fmt.Errorf("%s has an empty or malformed value", name)
			}
			if slices.Contains(values[:i], v) {
				return fmt.Errorf("%s repeats %q", name, v)
			}
		}
	}
	for _, e := range f.EventTypes {
		if e != EventStarted && e != EventFinished {
			return fmt.Errorf("event type %q is not run.started or run.finished", e)
		}
	}
	for _, s := range f.Statuses {
		if !slices.Contains(finishedStatuses, s) {
			return fmt.Errorf("status %q is not one of %s", s, strings.Join(finishedStatuses, ", "))
		}
	}
	if len(f.Statuses) > 0 && !slices.Contains(f.EventTypes, EventFinished) {
		return errors.New("statuses apply only to run.finished")
	}
	return nil
}

// Matches reports whether the filter selects an event with these values.
// Statuses constrain only run.finished.
func (f Filter) Matches(event, status, pipeline, repositoryKey string) bool {
	if !slices.Contains(f.EventTypes, event) {
		return false
	}
	if event == EventFinished && len(f.Statuses) > 0 && !slices.Contains(f.Statuses, status) {
		return false
	}
	if len(f.Pipelines) > 0 && !slices.Contains(f.Pipelines, pipeline) {
		return false
	}
	return len(f.Repositories) == 0 || slices.Contains(f.Repositories, repositoryKey)
}
