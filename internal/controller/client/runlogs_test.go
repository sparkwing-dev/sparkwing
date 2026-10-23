package client

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

func TestLogReaderUsesFinalRoutesAndAuthenticatedTransport(t *testing.T) {
	for _, tc := range []struct {
		path string
		call func(controllerwire.LogReader) error
	}{
		{"/api/v1/runs/412/logs?cursor=a%26b&events_only=true&tree=true", func(c controllerwire.LogReader) error {
			_, err := c.ReadLogs(t.Context(), "412", controllerwire.LogQuery{Cursor: "a&b", Tree: true, EventsOnly: true})
			return err
		}},
		{"/api/v1/runs/r1/jobs/build%2Flinux/logs?no_events=true&tail=0", func(c controllerwire.LogReader) error {
			zero := 0
			_, err := c.ReadLogs(t.Context(), "r1", controllerwire.LogQuery{Job: "build/linux", Tail: &zero, NoEvents: true})
			return err
		}},
		{"/api/v1/runs/r1/logs/search?grep=fatal&job=build&since=1h0m0s", func(c controllerwire.LogReader) error {
			_, err := c.ReadLogs(t.Context(), "r1", controllerwire.LogQuery{Job: "build", Grep: "fatal", Since: time.Hour})
			return err
		}},
		{"/api/v1/runs/grep?branch=main&branch=%21release&cursor=next&grep=fatal&job=build&limit=7&max_matches=0&pipeline=test&sha=abc&status=failed", func(c controllerwire.LogReader) error {
			_, err := c.SearchLogs(t.Context(), controllerwire.LogSearchQuery{Runs: controllerwire.RunQuery{Limit: 7, Cursor: "next", Pipeline: "test", Status: "failed", SHA: "abc", Branches: []string{"main", "!release"}}, Job: "build", Pattern: "fatal", MaxMatches: 0})
			return err
		}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			c, err := NewLogReader(EngineOptions{URL: "https://controller.example", Bearer: func(context.Context) (string, error) { return "private", nil }, HTTP: &http.Client{Transport: engineTransport(func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || req.URL.RequestURI() != tc.path || req.Header.Get("Authorization") != "Bearer private" {
					t.Fatalf("request = %s %s", req.Method, req.URL)
				}
				return engineResponse(200, `{"records":[],"next_cursor":"","truncated":false,"complete":true,"matches":[],"future":1}`), nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.call(c); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLogReaderRejectsInvalidAndIncompleteResponses(t *testing.T) {
	for _, body := range []string{
		`{"records":[],"next_cursor":"","truncated":false}`,
		`{"records":[],"next_cursor":"","truncated":true,"complete":false}`,
		`{"records":[],"next_cursor":"","truncated":false,"complete":false}`,
		`{"records":[],"next_cursor":"same","truncated":true,"complete":false}`,
		`{"records":[{"run_id":"r1","line_no":1,"line":"bad"}],"next_cursor":"","truncated":false,"complete":true}`,
	} {
		c, _ := NewLogReader(EngineOptions{URL: "https://controller.example", HTTP: &http.Client{Transport: engineTransport(func(*http.Request) (*http.Response, error) {
			return engineResponse(200, body), nil
		})}})
		if _, err := c.ReadLogs(t.Context(), "r1", controllerwire.LogQuery{Cursor: "same"}); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	c, _ := NewLogReader(EngineOptions{URL: "https://controller.example", HTTP: &http.Client{Transport: engineTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid query reached transport")
		return nil, nil
	})}})
	negative := -1
	for _, query := range []controllerwire.LogQuery{{Tail: &negative}, {Tail: new(int), Head: new(int)}, {Lines: "3:2"}, {EventsOnly: true, NoEvents: true}} {
		if _, err := c.ReadLogs(t.Context(), "r1", query); err == nil {
			t.Fatalf("accepted %+v", query)
		}
	}
	if _, err := c.SearchLogs(t.Context(), controllerwire.LogSearchQuery{}); err == nil {
		t.Fatal("accepted empty search")
	}
}

func TestLogReaderRetainsStructuredFailure(t *testing.T) {
	c := testEngine(t, func(*http.Request) (*http.Response, error) {
		return engineResponse(403, `{"error":{"code":"forbidden","message":"logs revoked","hint":"select a permitted profile"}}`), nil
	})
	_, err := c.ReadLogs(t.Context(), "r1", controllerwire.LogQuery{})
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Message != "logs revoked" || failure.Hint != "select a permitted profile" {
		t.Fatalf("failure = %v", err)
	}
}
