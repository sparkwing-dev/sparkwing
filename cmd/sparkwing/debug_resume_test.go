package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

type debugControlFake struct {
	controllerwire.DebugReader
	resume func(context.Context, string, string) error
}

func (f debugControlFake) ResumeDebugPause(ctx context.Context, run, job string) error {
	return f.resume(ctx, run, job)
}

func TestDebugResumeUsesObservedIdentityAndCommonOutput(t *testing.T) {
	for _, format := range []string{"json", "plain", "pretty"} {
		reader := debugReaderFixture()
		calls, closed := 0, false
		var out bytes.Buffer
		err := runDebugResumeArgs(t.Context(), []string{"--run=412", "--job=build", "--profile=prod", "-o=" + format}, &out, func(_ context.Context, profile string) (debugResumer, error) {
			if profile != "prod" {
				t.Fatal("changed controller")
			}
			return debugResumer{control: debugControlFake{DebugReader: reader, resume: func(_ context.Context, run, job string) error {
				calls++
				if run != "run-1" || job != "build" {
					t.Fatal("resume did not use observed identity")
				}
				return nil
			}}, close: func() error { closed = true; return nil }}, nil
		})
		if err != nil || calls != 1 || !closed {
			t.Fatalf("calls=%d closed=%v err=%v", calls, closed, err)
		}
		if format == "json" && out.String() != "{\"run_id\":\"run-1\",\"job_id\":\"build\",\"resumed\":true}\n" {
			t.Fatal(out.String())
		}
		if format == "plain" && out.String() != "run-1/build\n" {
			t.Fatal(out.String())
		}
	}
}

func TestDebugResumeUsesConfiguredController(t *testing.T) {
	path := profilesFixturePath(t)
	if err := writeConnectionFixture(path, "prod", "https://api.example", "private-resume-credential"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	profileHTTPFixture(t, func(r *http.Request) (int, string) {
		calls++
		if r.URL.Host != "api.example" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer private-resume-credential" {
			t.Fatalf("unexpected controller: %s %v", r.URL, r.Header)
		}
		switch calls {
		case 1:
			if r.Method != "GET" || r.URL.Path != "/api/v1/runs/412/jobs/build/debug-pause" {
				t.Fatalf("observation: %s %s", r.Method, r.URL)
			}
			return 200, `{"run_id":"run-1","number":412,"job_id":"build","pause":null}`
		case 2:
			body, err := io.ReadAll(r.Body)
			if err != nil || r.Method != "POST" || r.URL.Path != "/api/v1/runs/run-1/jobs/build/resume" || string(body) != "{}" {
				t.Fatalf("release: %s %s %s %v", r.Method, r.URL, body, err)
			}
			return http.StatusNoContent, ""
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
			return 500, ""
		}
	})
	var out bytes.Buffer
	err := runDebugResumeArgs(t.Context(), []string{"--run=412", "--job=build", "--profile=prod", "-o=json"}, &out, resolveDebugResumer)
	if err != nil || calls != 2 || out.String() != "{\"run_id\":\"run-1\",\"job_id\":\"build\",\"resumed\":true}\n" {
		t.Fatalf("calls=%d out=%s err=%v", calls, &out, err)
	}
}

func TestDebugResumeUnknownProfileHasNoStoreFallback(t *testing.T) {
	setProfilesFixture(t, "profiles: {}\n")
	home := filepath.Join(t.TempDir(), "absent-home")
	t.Setenv("SPARKWING_HOME", home)
	var out bytes.Buffer
	err := runDebugResumeArgs(t.Context(), []string{"--run=run-1", "--job=build", "--profile=missing", "-o=json"}, &out, resolveDebugResumer)
	if err == nil || !strings.Contains(err.Error(), `profile "missing" not found`) || out.Len() != 0 {
		t.Fatalf("out=%s err=%v", &out, err)
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing policy opened local state")
	}
}

func TestDebugResumePreservesWriterFailure(t *testing.T) {
	failure := &apierr.Error{Code: apierr.Forbidden, Message: "scope missing"}
	var out bytes.Buffer
	err := runDebugResumeArgs(t.Context(), []string{"--run=412", "--job=build", "-o=json"}, &out, func(context.Context, string) (debugResumer, error) {
		return debugResumer{control: debugControlFake{DebugReader: debugReaderFixture(), resume: func(context.Context, string, string) error { return failure }}, close: func() error { return nil }}, nil
	})
	if !errors.Is(err, failure) || out.Len() != 0 {
		t.Fatalf("out=%s err=%v", &out, err)
	}
}

func FuzzDebugResumeCannotSwitchIdentity(f *testing.F) {
	f.Add("412", "build", "run-1", "build")
	f.Add("other", "build", "run-1", "build")
	f.Fuzz(func(t *testing.T, selector, job, returnedRun, returnedJob string) {
		if strings.ContainsRune(selector, 0) || strings.ContainsRune(job, 0) {
			return
		}
		view := controllerwire.DebugPauseResult{RunID: returnedRun, Number: 412, JobID: returnedJob}
		reader := &debugReaderFake{pause: view}
		writes := 0
		err := runDebugResumeArgs(t.Context(), []string{"--run=" + selector, "--job=" + job, "-o=json"}, io.Discard, func(context.Context, string) (debugResumer, error) {
			return debugResumer{control: debugControlFake{DebugReader: reader, resume: func(_ context.Context, run, id string) error {
				writes++
				if view.Validate(selector, job) != nil || run != returnedRun || id != returnedJob {
					t.Fatal("resume switched observed identity")
				}
				return nil
			}}, close: func() error { return nil }}, nil
		})
		if err != nil && writes != 0 {
			t.Fatal("refused read still resumed a job")
		}
	})
}
