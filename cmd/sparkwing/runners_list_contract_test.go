package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

type runnerReaderFake func() (controllerwire.Runners, error)

func (f runnerReaderFake) ListRunners(context.Context) (controllerwire.Runners, error) { return f() }

func TestRunnerListingKeepsDetailsAndOutputModes(t *testing.T) {
	want := controllerwire.RunnerDetail{Name: "desktop", Class: "self_hosted", Location: "local", Status: "busy", Labels: []string{"gpu"}, ReservedPools: []string{"prod"}, Repos: []string{"acme/api"}, MaxConcurrent: 2, Metered: true, ActiveJobs: []controllerwire.RunnerJob{{RunID: "r1", JobID: "build"}}, Headroom: &controllerwire.RunnerHeadroom{RunnerResources: controllerwire.RunnerResources{Cores: 1.5, MemoryBytes: 2 << 30}, QueueDepth: 3, ObservedAt: "2026-10-14T01:02:03Z"}}
	reader := runnerReaderFake(func() (controllerwire.Runners, error) {
		return controllerwire.Runners{Runners: []controllerwire.RunnerDetail{want}}, nil
	})
	for _, format := range []string{"pretty", "json", "plain"} {
		for _, quiet := range []bool{false, true} {
			args := []string{"runners", "list", "-o", format}
			if quiet {
				args = append(args, "-q")
			}
			inv, err := clisurface.Parse("sparkwing", args)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := listRunners(t.Context(), inv, reader, &out); err != nil {
				t.Fatal(err)
			}
			switch {
			case format == "json" && !quiet:
				var got controllerwire.RunnerDetail
				if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.ReservedPools[0] != "prod" || got.Labels[0] != "gpu" || got.Headroom.QueueDepth != 3 || got.ActiveJobs[0].JobID != "build" || !got.Metered {
					t.Fatalf("detail: %+v %v", got, err)
				}
			case format == "json":
				var got map[string]string
				if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got) != 1 || got["name"] != "desktop" {
					t.Fatalf("quiet: %s %v", &out, err)
				}
			case format == "plain" || quiet:
				if out.String() != "desktop\n" {
					t.Fatalf("names: %q", &out)
				}
			default:
				for _, text := range []string{"desktop", "self_hosted", "prod", "gpu", "1.5c/2.0GiB", "1/2"} {
					if !strings.Contains(out.String(), text) {
						t.Fatalf("missing %s: %s", text, &out)
					}
				}
			}
		}
	}
}

func TestRunnerListingEmptyAndFailureAreUnambiguous(t *testing.T) {
	for _, format := range []string{"json", "plain"} {
		inv, err := clisurface.Parse("sparkwing", []string{"runners", "list", "-o", format})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		empty := runnerReaderFake(func() (controllerwire.Runners, error) { return controllerwire.Runners{}, nil })
		if err := listRunners(t.Context(), inv, empty, &out); err != nil || out.Len() != 0 {
			t.Fatalf("empty: %s %v", &out, err)
		}
		failure := errors.New("controller unavailable")
		failed := runnerReaderFake(func() (controllerwire.Runners, error) { return controllerwire.Runners{}, failure })
		if err := listRunners(t.Context(), inv, failed, &out); !errors.Is(err, failure) || out.Len() != 0 {
			t.Fatalf("failure: %s %v", &out, err)
		}
	}
}

func TestRunnerListingCommandUsesSelectedProfile(t *testing.T) {
	path := profilesFixturePath(t)
	if err := writeConnectionFixture(path, "prod", "https://controller.example", "private-credential"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	profileHTTPFixture(t, func(req *http.Request) (int, string) {
		calls++
		if req.Method != http.MethodGet || req.URL.Path != "/api/v1/runners" || req.Header.Get("Authorization") != "Bearer private-credential" {
			t.Fatalf("runner request: %s %s", req.Method, req.URL)
		}
		return 200, `{"runners":[{"name":"desktop","class":"self_hosted","location":"local","labels":["gpu"],"reserved_pools":["prod"],"repos":[],"last_seen":"","status":"idle","active_jobs":[],"max_concurrent":2,"capacity":{"cores":0,"memory_bytes":0},"metered":false}]}`
	})
	var result error
	out := captureStdout(t, func() {
		result = dispatchCommandArgs([]string{"runners", "list", "--profile", "prod", "-q", "-o", "json"})
	})
	if result != nil || calls != 1 || out != "{\"name\":\"desktop\"}\n" {
		t.Fatalf("runner command: %q %v calls=%d", out, result, calls)
	}
}
