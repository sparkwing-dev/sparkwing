package jobs

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func TestExampleProbeCleanupRemainsUntilTerminalObservation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("status fixture uses a POSIX executable")
	}
	for _, tc := range []struct {
		status string
		exit   int
		want   bool
	}{
		{"success", 0, true},
		{"failed", 0, true},
		{"cancelled", 0, true},
		{"running", 3, false},
		{"", 1, false},
	} {
		t.Run(fmt.Sprintf("%s-exit-%d", tc.status, tc.exit), func(t *testing.T) {
			cli := filepath.Join(t.TempDir(), "status")
			script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '{\"id\":\"probe\",\"status\":\"%s\"}'\nexit %d\n", tc.status, tc.exit)
			if err := os.WriteFile(cli, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			released := false
			run := exampleProbeRun{id: "probe", release: func() { released = true }}
			_, err := run.await(t.Context(), ExampleInputs{CLI: cli})
			if (err != nil) != (tc.exit == 1) || released != tc.want {
				t.Fatalf("released=%t, error=%v", released, err)
			}
		})
	}
}

func TestExampleOutputAssertionsReadJobAndStepPredecessors(t *testing.T) {
	host := sparkwingtest.New(sparkwingtest.Options{Outputs: map[string]json.RawMessage{"source": []byte(`"example-output-v1"`)}})
	defer func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	}()
	plan := sparkwing.NewPlan()
	source := sparkwing.Job(plan, "source", &exampleValue{Value: "example-output-v1"})
	sparkwing.Job(plan, "outputs", &exampleOutputJob{Source: sparkwing.RefTo[string](source)}).Needs(source)
	if _, err := host.RunStep(t.Context(), plan, "outputs", "predecessor"); err != nil {
		t.Fatal(err)
	}
	got, err := host.RunStep(t.Context(), plan, "outputs", "assert")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `"example-output-v1/step"` {
		t.Fatalf("output = %s", got)
	}
}

func TestExampleStatusReadsLastObservationAndRejectsMissingIdentity(t *testing.T) {
	got, err := exampleDecodeStatus("{\"id\":\"run\",\"status\":\"running\"}\n{\"id\":\"run\",\"status\":\"success\"}\n")
	if err != nil || got.Status != "success" {
		t.Fatalf("last observation = %+v, %v", got, err)
	}
	for _, raw := range []string{"", "{}", "{broken"} {
		if _, err := exampleDecodeStatus(raw); err == nil {
			t.Errorf("accepted invalid status %q", raw)
		}
	}
}

func TestExampleOIDCClaimsBindAudienceRunPipelineAndTime(t *testing.T) {
	expiry := time.Now().Add(time.Minute).Unix()
	payload := []byte(`{"iss":"https://issuer.example","sub":"team:example","aud":"sparkwing-example-gate","run_id":"run","pipeline":"example","iat":1,"exp":` + fmt.Sprint(expiry) + `}`)
	token := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
	if err := exampleVerifyClaims(token, "run", "example"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ token, run, pipeline string }{
		{token, "another-run", "example"}, {token, "run", "another-pipeline"}, {"not-a-token", "run", "example"},
	} {
		if err := exampleVerifyClaims(tc.token, tc.run, tc.pipeline); err == nil {
			t.Errorf("accepted mismatched claims: %+v", tc)
		}
	}
}

func TestExampleLifecycleProbesSkipOnlyWithoutARunnableCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture CLI is a POSIX executable")
	}
	host := sparkwingtest.New(sparkwingtest.Options{})
	defer func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx := host.Context(t.Context())
	cli := filepath.Join(t.TempDir(), "sparkwing")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if exampleNeedsCLI(ExampleInputs{CLI: cli})(ctx) {
		t.Fatal("skipped probes although the CLI is runnable")
	}
	if !exampleNeedsCLI(ExampleInputs{CLI: filepath.Join(t.TempDir(), "absent")})(ctx) {
		t.Fatal("ran probes without a runnable CLI")
	}
}
