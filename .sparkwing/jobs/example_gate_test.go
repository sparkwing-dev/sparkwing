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
			ctx := newTestHost(t, sparkwingtest.Options{}).Context(t.Context())
			_, err := run.await(ctx, ExampleInputs{CLI: cli})
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
	rc := sparkwing.RunContext{RunID: "run-lifecycle-" + filepath.Base(t.TempDir())}
	if exampleNeedsCLI(ExampleInputs{CLI: cli}, rc)(ctx) {
		t.Fatal("skipped probes although the CLI is runnable")
	}
	absent := ExampleInputs{CLI: filepath.Join(t.TempDir(), "absent")}
	if !exampleNeedsCLI(absent, rc)(ctx) {
		t.Fatal("ran probes without a runnable CLI")
	}
	dir := exampleCLIDir(rc)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "sparkwing")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if exampleNeedsCLI(absent, rc)(ctx) {
		t.Fatal("skipped probes although lifecycle-cli built a CLI")
	}
	if got, ok := exampleLifecycle(absent, rc); !ok || got.CLI != wrapper || got.APIProfile != exampleProbeProfile {
		t.Fatalf("lifecycle CLI = %+v, %t", got, ok)
	}
	if got, _ := exampleLifecycle(ExampleInputs{CLI: cli}, rc); got.CLI != cli || got.APIProfile != "" {
		t.Fatalf("a CLI on the host was replaced: %+v", got)
	}
}

func TestExampleBuildCLISignsInBeforePublishingTheWrapper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture toolchain is a POSIX executable")
	}
	t.Chdir(t.TempDir())
	tools := t.TempDir()
	record := filepath.Join(tools, "record")
	fakeCLI := fmt.Sprintf("#!/bin/sh\\n{ echo \"home=$SPARKWING_HOME\"; echo \"args=$*\"; echo \"token=$(cat)\"; } > \"%s\"\\n", record)
	fakeGo := fmt.Sprintf("#!/bin/sh\nwhile [ \"$1\" != -o ]; do shift; done\nprintf '%s' > \"$2\"\nchmod 0700 \"$2\"\n", fakeCLI)
	if err := os.WriteFile(filepath.Join(tools, "go"), []byte(fakeGo), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	host := sparkwingtest.New(sparkwingtest.Options{Secrets: map[string]sparkwingtest.Secret{exampleProbeToken: {Value: "probe-token", Masked: true}}})
	defer func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	}()
	rc := sparkwing.RunContext{RunID: "run-build-cli"}
	in := ExampleInputs{CLI: filepath.Join(tools, "absent"), APIController: "https://staging.example"}
	if err := exampleBuildCLI(in, rc)(host.Context(t.Context())); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(exampleCLIDir(rc), "home")
	want := "home=" + home + "\nargs=profiles add --profile " + exampleProbeProfile + " --controller https://staging.example --token-stdin\ntoken=probe-token\n"
	if string(got) != want {
		t.Fatalf("profile add saw\n%s\nwant\n%s", got, want)
	}
	if _, err := os.Stat(filepath.Join(home, "token")); !os.IsNotExist(err) {
		t.Fatalf("token file left behind: %v", err)
	}
	if resolved, ok := exampleLifecycle(in, rc); !ok || resolved.APIProfile != exampleProbeProfile {
		t.Fatalf("probes cannot resolve the built CLI: %+v", resolved)
	}
}
