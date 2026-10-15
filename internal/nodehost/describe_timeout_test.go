//go:build unix

package nodehost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const testHelloScript = `IFS= read -r line <&3
id=$(printf '%s' "$line" | sed 's/.*"id":"\([^"]*\)".*/\1/')
printf '{"reply":"%s","ok":true,"result":{"protocol":2,"sdk":{"language":"go","version":"test"},"features":[]}}\n' "$id" >&4
`

type planningStore struct {
	*store.Store
	createErr, snapshotErr error
	created                []store.Run
	finished               []string
	snapshots              int
}

func (s *planningStore) CreateRun(_ context.Context, run store.Run) error {
	s.created = append(s.created, run)
	return s.createErr
}

func (s *planningStore) UpdatePlanSnapshot(context.Context, string, []byte) error {
	s.snapshots++
	return s.snapshotErr
}

func (s *planningStore) FinishRun(_ context.Context, _, status, _ string) error {
	s.finished = append(s.finished, status)
	return nil
}

func TestPlanningRefusalsPreserveTheRunRecordBoundary(t *testing.T) {
	for _, phase := range []string{"arguments", "secrets", "create", "plan", "snapshot", "protocol"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			entry, marker := filepath.Join(dir, "planner"), filepath.Join(dir, "planned")
			result := `"ok":true,"result":{"jobs":[{"id":"job","steps":[{"id":"body"}]}]}`
			if phase == "plan" {
				result = `"ok":false,"error":{"code":"sdk_error","message":"plan refused"}`
			}
			script := "#!/bin/sh\n" + testHelloScript + `IFS= read -r line <&3
id=$(printf '%s' "$line" | sed 's/.*"id":"\([^"]*\)".*/\1/')
printf '{"reply":"%s","ok":true,"result":{"pipelines":[{"name":"demo","args":[{"name":"label","default":"resolved"}],"secrets":[{"name":"required"}]}]}}\n' "$id" >&4
IFS= read -r line <&3 || exit 0
printf . > "$PLAN_MARKER"
id=$(printf '%s' "$line" | sed 's/.*"id":"\([^"]*\)".*/\1/')
printf '{"reply":"%s",` + result + `}\n' "$id" >&4
IFS= read -r line <&3
`
			if phase == "protocol" {
				script += "printf 'not a frame\\n' >&4\n"
			}
			script += "exit 0\n"
			if err := os.WriteFile(entry, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			refused := errors.New("refused " + phase)
			state := &planningStore{Store: openStore(t)}
			cfg := Config{
				Session: SessionConfig{
					Entry: pipelineentry.Binary(entry), Dir: dir,
					Env:    []string{"PATH=/usr/bin:/bin", "PLAN_MARKER=" + marker},
					Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
					Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}},
				},
				Pipeline: "demo", Run: nodeproto.RunContext{RunID: "run", Pipeline: "demo"},
				State: state, Attempts: &Attempts{Masker: secrets.NewMasker()},
				CheckArgs: func(args map[string]string) error {
					if args["label"] != "resolved" {
						t.Fatalf("unresolved arguments: %v", args)
					}
					if phase == "arguments" {
						return refused
					}
					return nil
				},
				CheckSecrets: func(_ context.Context, names []string) error {
					if len(names) != 1 || names[0] != "required" {
						t.Fatalf("secret preflight lost declarations: %v", names)
					}
					if phase == "secrets" {
						return refused
					}
					return nil
				},
			}
			state.snapshotErr = refused
			if phase == "create" {
				state.createErr = refused
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			resultRun, err := Execute(ctx, cfg)
			recorded := phase == "plan" || phase == "snapshot" || phase == "protocol"
			if recorded {
				if err != nil || resultRun.Status != "failed" || resultRun.Err == nil || len(state.finished) != 1 || state.finished[0] != "failed" {
					t.Fatalf("recorded failure: result=%+v error=%v finishes=%v", resultRun, err, state.finished)
				}
			} else if err == nil || resultRun.RunID != "" || len(state.finished) != 0 {
				t.Fatalf("pre-record refusal: result=%+v error=%v finishes=%v", resultRun, err, state.finished)
			}
			wantCreated := 1
			if phase == "arguments" || phase == "secrets" {
				wantCreated = 0
			}
			if len(state.created) != wantCreated || wantCreated == 1 && state.created[0].Args["label"] != "resolved" {
				t.Fatalf("run records=%+v", state.created)
			}
			_, markerErr := os.Stat(marker)
			if recorded != (markerErr == nil) || markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
				t.Fatalf("planning crossed a refused record boundary: %v", markerErr)
			}
			wantSnapshots := 0
			if phase == "snapshot" {
				wantSnapshots = 1
			}
			if state.snapshots != wantSnapshots {
				t.Fatalf("plan snapshots=%d, want %d", state.snapshots, wantSnapshots)
			}
		})
	}
}

func TestAStalledDescribeFinishesItsOwnerAndJoinsChildren(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "stall-describe")
	hello := strings.ReplaceAll(testHelloScript, `"version":"test"`, `"version":"v1.0.0"`)
	script := "#!/bin/sh\n" + hello + fmt.Sprintf("trap '' TERM\n/bin/sleep 3600 &\necho $! > %s/child\nIFS= read -r line <&3\nwait\n", dir)
	if err := os.WriteFile(entry, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	state, host, input := newOwnerFixture(t, false)
	acquire := host.Acquire
	host.Acquire = func(ctx context.Context, input RunInput) (*source.Entry, error) {
		code, err := acquire(ctx, input)
		code.Command, code.Directory = []string{entry}, dir
		return code, err
	}
	host.Prepare = func(ctx context.Context, input RunInput, code *source.Entry) (OwnerPlanning, error) {
		bounded, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		config := ProtocolSessionConfig{Environments: sdkTestEnvironment(nil)}
		config.Environments.Base.Captured = []string{"PATH=/usr/bin:/bin"}
		planner := SDKPlanner{
			Start: config.StartPlanning, Masker: secrets.NewMasker(),
			EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
				return code.Engine, nil
			},
			Log: func(context.Context, nodeproto.FrameLog) error { return nil },
		}
		_, err := planner.Prepare(bounded, PlanningProcessInput{
			Entry: code, Runner: input.GrantedRunner, Run: nodeproto.RunContext{RunID: input.RunID, Pipeline: "demo"},
		}, PlanningInput{})
		return OwnerPlanning{}, err
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, err := host.Run(ctx, input)
	if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		t.Fatalf("err = %v, want the describe deadline", err)
	}
	if state.accepted.RunID != "" || state.finished.Unit != input.Owner.Owner || state.finished.Transition.To != store.RunFailed {
		t.Fatalf("stalled describe accepted topology or left its owner unfinished: %+v", state.finished)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "child"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		if f := strings.Fields(string(stat)); len(f) > 2 && f[2] != "Z" {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the stalled process's child %d outlived the run", pid)
		}
	}
}

// hack: one helper drives both planning entry points, Execute and
// PlanDocument, with a shell entry whose describe fails.
func describeFails(t *testing.T, script, via string, sink Sink) error {
	t.Helper()
	dir := t.TempDir()
	entry := filepath.Join(dir, "describe-fails")
	if err := os.WriteFile(entry, []byte("#!/bin/sh\n"+testHelloScript+script), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := SessionConfig{
		Entry: pipelineentry.Binary(entry), Dir: dir, Env: []string{"PATH=/usr/bin:/bin"},
		Grace: 50 * time.Millisecond, DescribeTimeout: 300 * time.Millisecond, Sink: sink, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var err error
	if via == "execute" {
		_, err = Execute(ctx, Config{
			Session: cfg, Pipeline: "demo", Run: nodeproto.RunContext{RunID: "describe-fails", Pipeline: "demo"},
			State: openStore(t), Logger: slog.New(slog.DiscardHandler), Attempts: &Attempts{Masker: secrets.NewMasker()},
		})
	} else {
		_, err = PlanDocument(ctx, cfg, PlanRequest{Pipeline: "demo", Run: nodeproto.RunContext{RunID: "describe-fails", Pipeline: "demo"}, Masker: secrets.NewMasker()})
	}
	if err == nil {
		t.Fatal("describe succeeded")
	}
	if ctx.Err() != nil {
		t.Fatalf("planning outlived its bound: %v", err)
	}
	return err
}

var describeVias = []string{"execute", "plan"}

// A plan process that prints a secret default and then dies or stalls before
// describe answers leaves the host no way to know the secret, so its output is
// withheld whole and the sink is told so.
func TestAFailedDescribeWithholdsWhatThePlanProcessPrinted(t *testing.T) {
	const secret = "def-describe-secret"
	for name, tail := range map[string]string{"crash": "exit 3\n", "hang": "IFS= read -r line <&3\n/bin/sleep 3600\n"} {
		for _, via := range describeVias {
			t.Run(name+"/"+via, func(t *testing.T) {
				sink := &planSinkText{masker: secrets.NewMasker()}
				describeFails(t, "echo plan sees def="+secret+"\n"+tail, via, sink)
				if got := sink.String(); strings.Contains(got, secret) || !strings.Contains(got, "output withheld: describe failed before secrets were known") {
					t.Fatalf("plan output = %q, want it withheld with a notice", got)
				}
			})
		}
	}
}

// A describe the SDK answers with an error, as a ShortHelp that panics quoting
// a secret default does, returns its class but not its text.
func TestAFailedDescribeWithholdsItsErrorText(t *testing.T) {
	const secret = "def-describe-secret"
	script := `IFS= read -r line <&3
id=$(printf '%s' "$line" | sed 's/.*"id":"\([^"]*\)".*/\1/')
printf '{"reply":"%s","ok":false,"error":{"code":"plan_failed","message":"describe panicked: ` + secret + `"}}\n' "$id" >&4
IFS= read -r line <&3
`
	for _, via := range describeVias {
		t.Run(via, func(t *testing.T) {
			err := describeFails(t, script, via, discardSink{})
			var perr *apierr.Error
			if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "describe failed: plan_failed") || !errors.As(err, &perr) {
				t.Fatalf("err = %v, want its class without its text", err)
			}
		})
	}
}

type stalledSink struct{ unblock chan struct{} }

func (s stalledSink) Text(string, string)       { <-s.unblock }
func (s stalledSink) Record(nodeproto.FrameLog) { <-s.unblock }

// A sink that never returns cannot hold planning past the session's grace,
// whether describe's output is released or withheld.
func TestAStalledSinkDoesNotHoldPlanningOpen(t *testing.T) {
	for _, via := range describeVias {
		t.Run(via, func(t *testing.T) {
			sink := stalledSink{unblock: make(chan struct{})}
			defer close(sink.unblock)
			describeFails(t, "echo one\necho two\nexit 3\n", via, sink)
		})
	}
}

// Every line describe's output held counts against the cap and in the notice,
// empty ones and those the cap dropped included.
func TestTheWithheldNoticeCountsEveryLine(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "describe-fails")
	script := "#!/bin/sh\n" + testHelloScript + "i=0; while [ $i -lt 140000 ]; do echo; i=$((i+1)); done\nexit 3\n"
	if err := os.WriteFile(entry, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	sink := &planSinkText{masker: secrets.NewMasker()}
	// hack: the stalled-describe deadline can interrupt the output loop before all lines reach the sink.
	_, err := PlanDocument(t.Context(), SessionConfig{
		Entry: pipelineentry.Binary(entry), Dir: dir, Env: []string{"PATH=/usr/bin:/bin"}, Sink: sink, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
	}, PlanRequest{Pipeline: "demo", Run: nodeproto.RunContext{RunID: "describe-fails", Pipeline: "demo"}, Masker: secrets.NewMasker()})
	var perr *apierr.Error
	if !errors.As(err, &perr) || perr.Code != apierr.SDKExited {
		t.Fatalf("err = %v, want the process to exit after writing every line", err)
	}
	if got := sink.String(); !strings.Contains(got, "(140000 lines)") {
		t.Fatalf("plan output = %q, want a notice counting all 140000 lines", got[max(0, len(got)-200):])
	}
}
