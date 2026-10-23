package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestRawStdoutCountsAsProgress(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	var progress int
	var output []nodeproto.FrameLog
	logs := &planningLogs{known: true, masker: secrets.NewMasker(), sink: func(_ context.Context, frame nodeproto.FrameLog) error { output = append(output, frame); return nil }}
	session := &protocolSession{logCtx: t.Context(), cfg: ProtocolSessionConfig{output: logs, Activity: ExecutorActivity{Progress: func() { progress++ }}}}
	session.streams.Add(1)
	go session.readProtocolText(r, "info")
	if _, err := w.Write([]byte("raw line\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	session.streams.Wait()
	if progress == 0 || len(output) != 1 || output[0].Message != "raw line" || output[0].TS == "" {
		t.Fatalf("progress=%d output=%+v", progress, output)
	}
	if err := logs.finish(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestIdleOutputFlushesOnlySafePartialLines(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		masker := secrets.NewMasker()
		if err := masker.Register("private-token"); err != nil {
			t.Fatal(err)
		}
		var frames []nodeproto.FrameLog
		logs := planningLogs{known: true, masker: masker, sink: func(_ context.Context, frame nodeproto.FrameLog) error { frames = append(frames, frame); return nil }}
		if err := logs.text(t.Context(), "info", "safe pri", false); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Millisecond)
		synctest.Wait()
		if len(frames) != 1 || frames[0].Message != "safe " {
			t.Fatalf("idle output = %+v", frames)
		}
		if err := logs.text(t.Context(), "info", "vate-token\n", false); err != nil {
			t.Fatal(err)
		}
		if len(frames) != 2 || frames[1].Message != "***" {
			t.Fatalf("tail escaped = %+v", frames)
		}
		if err := logs.finish(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestHostMasksBytesBeforeLineSplitting(t *testing.T) {
	masker := secrets.NewMasker()
	if err := masker.Register("first-line\nsecond-line"); err != nil {
		t.Fatal(err)
	}
	for _, gated := range []bool{false, true} {
		var output []string
		logs := planningLogs{known: !gated, masker: masker, sink: func(_ context.Context, frame nodeproto.FrameLog) error {
			output = append(output, frame.Message)
			return nil
		}}
		chunks := []string{"safe\nfirst-", "line\nsecond-", "line\nafter"}
		for _, chunk := range chunks {
			if err := logs.text(t.Context(), "info", chunk, false); err != nil {
				t.Fatal(err)
			}
		}
		if err := logs.text(t.Context(), "info", "", true); err != nil {
			t.Fatal(err)
		}
		if gated {
			if len(output) != 0 {
				t.Fatal("released bytes before discovery")
			}
			if err := logs.release(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if got := strings.Join(output, "\n"); got != "safe\n***\nafter" {
			t.Fatalf("output=%q", got)
		}
	}
}

func TestHostStreamSinkFailureStopsOutput(t *testing.T) {
	failure := errors.New("log sink failed")
	logs := planningLogs{known: true, masker: secrets.NewMasker(), sink: func(context.Context, nodeproto.FrameLog) error { return failure }}
	if err := logs.text(t.Context(), "info", "line\n", false); !errors.Is(err, failure) {
		t.Fatalf("lost sink failure: %v", err)
	}
	if err := logs.finish(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("lost finish failure: %v", err)
	}
}

func TestGeneratedDeclarationsRegisterRawDefaultsAndListItemsBeforeRelease(t *testing.T) {
	masker := secrets.NewMasker()
	var output strings.Builder
	logs := planningLogs{known: true, masker: masker, sink: func(_ context.Context, frame nodeproto.FrameLog) error { output.WriteString(frame.Message); return nil }}
	session := protocolSession{cfg: ProtocolSessionConfig{output: &logs, Masks: NewMaskRegistry(masker)}}
	raw := " first-private , second-private "
	spec := runspec.Spec{Request: runspec.SealedRequest{Request: runspec.Request{RequestedArgs: map[string]string{"later": raw}}}, Resolved: runspec.Resolved{Args: map[string]string{"later": "first-private,second-private"}}}
	def := "abc, default-first, default-second"
	result := nodeproto.EvalResult{DeclarationsKnown: true, Arguments: []nodeproto.ArgumentDescription{{Name: "later", Type: "[]string", Secret: true, Default: &def}}}
	session.gateDeclarations()
	if err := logs.write(t.Context(), nodeproto.FrameLog{Message: raw + def + " first-private second-private"}); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatal("generator output escaped the gate")
	}
	if err := session.releaseDeclarations(t.Context(), spec, result); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "private") || strings.Contains(output.String(), "default") {
		t.Fatalf("unmasked output %q", output.String())
	}
	if masker.Mask("abc") != "abc" || strings.Contains(output.String(), secrets.ShortValueWarning) {
		t.Fatalf("short generator default masked or warned: %s", &output)
	}
	projection, err := generatorRegistration(spec, result.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	fresh := secrets.NewMasker()
	for _, value := range projection.Values {
		if err := fresh.Register(value); err != nil {
			t.Fatal(err)
		}
	}
	if fresh.Mask(raw+def+"first-private second-private") != masker.Mask(raw+def+"first-private second-private") {
		t.Fatal("fresh host lost generator registration semantics")
	}
}

func TestFailedGeneratorWithholdsUnclassifiedOutputAndError(t *testing.T) {
	var output strings.Builder
	masker := secrets.NewMasker()
	logs := planningLogs{known: true, masker: masker, sink: func(_ context.Context, frame nodeproto.FrameLog) error { output.WriteString(frame.Message); return nil }}
	session := protocolSession{cfg: ProtocolSessionConfig{output: &logs, Masks: NewMaskRegistry(masker)}}
	session.gateDeclarations()
	if err := logs.text(t.Context(), "info", "unknown-private-value\n", false); err != nil {
		t.Fatal(err)
	}
	err := session.releaseDeclarations(t.Context(), runspec.Spec{}, nodeproto.EvalResult{})
	if err == nil || !strings.Contains(err.Error(), "withheld") {
		t.Fatal("failed generator did not withhold error details")
	}
	if err := logs.finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "unknown-private-value") {
		t.Fatal("failed generator released its output")
	}
}

func TestResumedOwnerLoadsAcceptedExpansionRegistrations(t *testing.T) {
	state, _, _ := newOwnerFixture(t, true)
	projection, err := runspec.BuildSecretRegistration(runspec.Request{}, runspec.Resolved{SecretRegistration: runspec.SecretRegistration{Defaults: []runspec.SecretValue{{Raw: "generated-private"}}}})
	if err != nil {
		t.Fatal(err)
	}
	state.snapshot.Expansions = append(state.snapshot.Expansions, store.AcceptedExpansionRow{Registration: projection})
	accepted, err := ownerAccepted(state.snapshot.Run)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewMaskRegistry(secrets.NewMasker())
	if err := registry.initializeSnapshot(state.snapshot, accepted); err != nil {
		t.Fatal(err)
	}
	if !registry.ContainsSecret("generated-private") {
		t.Fatal("resumed owner lost generated registrations")
	}
}

func TestPlanningFailureMasksCanonicalPayloads(t *testing.T) {
	masker := secrets.NewMasker()
	if err := masker.Register("private-default"); err != nil {
		t.Fatal(err)
	}
	planner := SDKPlanner{Masker: masker}
	source := &apierr.Error{Code: "failure-private-default", Message: "private-default", Hint: "private-default", Details: map[string]any{"token": "private-default"}}
	var result *apierr.Error
	if !errors.As(planner.planningError(t.Context(), source, true), &result) {
		t.Fatal("lost canonical error")
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "private-default") || result.Code != apierr.Internal {
		t.Fatalf("unmasked planning error: %s", raw)
	}
}

func TestPlanningCannotProceedWithUnknownPipelineDeclarations(t *testing.T) {
	planner, input, fixture, processes := sdkPlanningFixture(t)
	start := planner.Start
	planner.Start = func(ctx context.Context, in PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
		process, err := start(ctx, in, log)
		proc := process.(*executorProcess)
		answer := proc.answer
		proc.answer = func(ctx context.Context, request nodeproto.Request) (any, error) {
			if request.Op == nodeproto.OpDescribe {
				return nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{{Name: input.Entrypoint, Args: []nodeproto.ArgumentDescription{}}, {Name: "broken", Error: &apierr.Error{Code: apierr.SDKError, Message: "unknown-private"}}}}, nil
			}
			return answer(ctx, request)
		}
		return process, err
	}
	_, err := planner.Prepare(t.Context(), input, fixture.input)
	if err == nil || strings.Contains(err.Error(), "unknown-private") {
		t.Fatalf("incomplete discovery error=%v", err)
	}
	if len(*processes) != 1 {
		t.Fatal("started author plan after incomplete discovery")
	}
}
