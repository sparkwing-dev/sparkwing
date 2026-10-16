package controller

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
)

func TestRunnerPolicyBuildKinds(t *testing.T) {
	for _, version := range []string{"(devel)", "(unknown)", "v1.2.3", ""} {
		t.Run(version, func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			requests := 0
			resolver, err := engine.NewResolver(engine.Config{CacheDirectory: t.TempDir(), HTTPClient: &http.Client{Transport: intakeGitHubTransport(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.URL.String() != engine.VersionIndexURL {
					t.Errorf("unexpected policy URL %s", r.URL)
				}
				return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			policy := &SignedRunnerAdmission{Resolver: resolver, BuildVersion: version}
			development := version == "(devel)" || version == "(unknown)"
			for range 2 {
				err := policy.CheckRunner(t.Context(), "v1.2.3")
				if development && err != nil || !development && (err == nil || !strings.Contains(err.Error(), "HTTP 403")) {
					t.Fatalf("build %q policy result: %v", version, err)
				}
			}
			if development {
				if requests != 0 || strings.Count(output.String(), "policy checks are off for a development build") != 1 {
					t.Fatalf("development policy: requests=%d log=%q", requests, output.String())
				}
			} else if requests != 2 || output.Len() != 0 {
				t.Fatalf("release policy: requests=%d log=%q", requests, output.String())
			}
		})
	}
}

func TestReleasedBuildRunnerInputCannotDisablePolicy(t *testing.T) {
	policy := &SignedRunnerAdmission{BuildVersion: "v1.2.3"}
	if err := quick.Check(func(runnerVersion string) bool {
		return policy.CheckRunner(t.Context(), runnerVersion) != nil
	}, nil); err != nil {
		t.Fatal(err)
	}
}
