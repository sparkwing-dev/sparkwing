package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestCollectRunIDs_FlagsAndStdinDash(t *testing.T) {
	stdin := strings.NewReader("run-stdin-1\nrun-stdin-2\n")
	got, err := collectRunIDs(
		[]string{"run-flag-a", "-", "run-flag-b"},
		stdin,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run-flag-a", "run-flag-b", "run-stdin-1", "run-stdin-2"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}

func TestRunActionNumericSelectorsReachSharedTransport(t *testing.T) {
	for _, action := range []string{"cancel", "retry", "prune"} {
		t.Run(action, func(t *testing.T) {
			var paths []string
			options := client.EngineOptions{URL: "https://controller.example", Bearer: func(context.Context) (string, error) { return "credential", nil }, HTTP: &http.Client{Transport: logRoundTrip(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Authorization") != "Bearer credential" {
					t.Fatal("run action lost authenticated transport")
				}
				paths = append(paths, req.URL.Path)
				status, body := http.StatusOK, `{"state_seq":1,"generation":0,"attempt":0,"replayed":false,"cancel_requested":true}`
				if action == "retry" {
					body = `{"error":{"code":"forbidden","message":"retry refused"}}`
					status = http.StatusForbidden
				} else if action == "prune" {
					status, body = http.StatusNoContent, ""
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}}
			ids, err := collectRunIDs([]string{"42", "-"}, strings.NewReader("42\nrun-42\n"))
			if err != nil {
				t.Fatal(err)
			}
			controller, err := client.NewEngineClient(options)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			suffix := "/" + action
			switch action {
			case "cancel":
				err = cancelRunIDs(t.Context(), controller, ids, &out, "json")
			case "retry":
				err = retryRunIDs(t.Context(), retryConnection{Controller: controller}, ids, controllerwire.RetryFailed, &out, "json")
			case "prune":
				pruner, openErr := client.NewRunPruner(options)
				if openErr != nil {
					t.Fatal(openErr)
				}
				err = pruneObservedRuns(t.Context(), pruneInvocation(t, "--run", "-", "-o", "json"), pruner, strings.NewReader("42\n42\nrun-42\n"), &out, time.Now())
				suffix = ""
			}
			want := []string{"/api/v1/runs/42" + suffix, "/api/v1/runs/run-42" + suffix}
			if !reflect.DeepEqual(paths, want) || (err != nil) != (action == "retry") {
				t.Fatalf("run action paths=%v want=%v err=%v", paths, want, err)
			}
		})
	}
}

func FuzzRunActionSelectorsPreserveIdentity(f *testing.F) {
	for _, selector := range []string{"1", "42", "run-42", "stable-id", "01", "+1", " 42 ", "foreign?team=b", "..", "雪"} {
		f.Add(selector)
	}
	f.Fuzz(func(t *testing.T, selector string) {
		if len(selector) > 4096 || strings.ContainsAny(selector, "\r\n") || strings.TrimSpace(selector) == "" || selector == "-" {
			t.Skip()
		}
		want := strings.TrimSpace(selector)
		for _, fromStdin := range []bool{false, true} {
			flags, input := []string{selector, selector}, ""
			if fromStdin {
				flags, input = []string{"-"}, selector+"\n"+selector+"\n"
			}
			got, err := collectRunIDs(flags, strings.NewReader(input))
			if err != nil || len(got) != 1 || got[0] != want {
				t.Fatalf("selector %q stdin=%t became %q: %v", selector, fromStdin, got, err)
			}
		}
	})
}

func TestCollectRunIDs_NoDashMeansNoStdinRead(t *testing.T) {
	stdin := strings.NewReader("should-not-be-read\n")
	got, err := collectRunIDs([]string{"run-a"}, stdin)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "run-a" {
		t.Errorf("got %v, want [run-a]", got)
	}
}

func TestCollectRunIDs_Dedup(t *testing.T) {
	stdin := strings.NewReader("run-a\nrun-b\n")
	got, err := collectRunIDs([]string{"run-a", "-"}, stdin)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("dedup failed: %v", got)
	}
}

func TestCollectRunIDs_SkipsBlankStdinLines(t *testing.T) {
	stdin := strings.NewReader("run-a\n\n   \nrun-b\n")
	got, err := collectRunIDs([]string{"-"}, stdin)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 entries", got)
	}
}

func TestCollectRunIDs_EmptyReturnsEmpty(t *testing.T) {
	got, err := collectRunIDs(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}
