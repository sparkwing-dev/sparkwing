package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

type debugReaderFake struct {
	job       controllerwire.DebugJob
	pause     controllerwire.DebugPauseResult
	jobErr    error
	pauseErr  error
	calls     []string
	afterRead func()
}

func (f *debugReaderFake) ReadDebugJob(_ context.Context, run, job string) (controllerwire.DebugJob, error) {
	f.calls = append(f.calls, "job:"+run+":"+job)
	if f.afterRead != nil {
		f.afterRead()
	}
	return f.job, f.jobErr
}

func (f *debugReaderFake) ReadDebugPause(_ context.Context, run, job string) (controllerwire.DebugPauseResult, error) {
	f.calls = append(f.calls, "pause:"+run+":"+job)
	return f.pause, f.pauseErr
}

func debugReaderFixture() *debugReaderFake {
	return &debugReaderFake{
		job:   controllerwire.DebugJob{RunID: "run-1", Number: 412, Job: controllerwire.JobDetail{ID: "build", Status: "running", Attempt: 3}, Execution: &controllerwire.DebugExecution{Runner: "cloud-1", Kind: "kubernetes", Namespace: "ci", Pod: "job-1", Container: "runner"}},
		pause: controllerwire.DebugPauseResult{RunID: "run-1", Number: 412, JobID: "build", Pause: &controllerwire.DebugPause{Reason: "before", PausedAt: time.Date(2026, 10, 15, 1, 2, 3, 0, time.UTC)}},
	}
}

func debugInvocation(t *testing.T, verb string, extra ...string) clisurface.Invocation {
	t.Helper()
	inv, err := clisurface.Parse("sparkwing", append([]string{"debug", verb, "--run=412", "--job=build", "--output=json"}, extra...))
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func TestDebugEnvReadsSharedProjectionInEveryFormat(t *testing.T) {
	for _, format := range []string{"json", "plain", "pretty"} {
		t.Run(format, func(t *testing.T) {
			fake := debugReaderFixture()
			inv := debugInvocation(t, "env")
			inv.Flags["output"] = []string{format}
			var out bytes.Buffer
			if err := observeDebug(t.Context(), inv, fake, &out, nil); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fake.calls, []string{"job:412:build", "pause:run-1:build"}) {
				t.Fatalf("selector resolution: %v", fake.calls)
			}
			if format == "json" {
				var got debugObservation
				if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got.DebugJob, fake.job) || !reflect.DeepEqual(got.Pause, fake.pause.Pause) {
					t.Fatalf("projection changed: %+v %v", got, err)
				}
			} else {
				for _, want := range []string{"run: run-1", "job: build", "attempt: 3", "runner: cloud-1 (kubernetes)", "reason=before", "expires=never"} {
					if !strings.Contains(out.String(), want) {
						t.Fatalf("missing %q: %s", want, out.String())
					}
				}
			}
		})
	}
}

func TestDebugEnvDoesNotHidePauseReadFailures(t *testing.T) {
	denial := &apierr.Error{Code: apierr.Forbidden, Message: "refused"}
	for _, change := range []func(*debugReaderFake){
		func(f *debugReaderFake) { f.jobErr = denial },
		func(f *debugReaderFake) { f.pauseErr = denial },
		func(f *debugReaderFake) { f.pause.RunID = "other" },
		func(f *debugReaderFake) { f.pause.Number++ },
		func(f *debugReaderFake) { f.job.Job.ID = "other" },
	} {
		fake := debugReaderFixture()
		change(fake)
		var out bytes.Buffer
		if err := observeDebug(t.Context(), debugInvocation(t, "env"), fake, &out, nil); err == nil || out.Len() != 0 {
			t.Fatalf("read failure became an observation: %v %s", err, out.String())
		}
	}
	fake := debugReaderFixture()
	fake.pause.Pause = nil
	var out bytes.Buffer
	if err := observeDebug(t.Context(), debugInvocation(t, "env"), fake, &out, nil); err != nil || !strings.Contains(out.String(), `"pause":null`) {
		t.Fatalf("explicit absence: %v %s", err, out.String())
	}
}

func TestDebugAttachUsesExplicitExecutionAndNamespace(t *testing.T) {
	for _, namespace := range []string{"", "other-ci"} {
		fake := debugReaderFixture()
		var extra []string
		if namespace != "" {
			extra = []string{"--namespace=" + namespace}
		}
		var out bytes.Buffer
		called := false
		err := observeDebug(t.Context(), debugInvocation(t, "attach", extra...), fake, &out, func(_ context.Context, target controllerwire.DebugExecution, _ io.Writer) error {
			called = true
			want := *fake.job.Execution
			if namespace != "" {
				want.Namespace = namespace
			}
			if target != want {
				t.Fatalf("attachment target changed: %+v", target)
			}
			return nil
		})
		if err != nil || !called || len(fake.calls) != 1 || fake.job.Execution.Namespace != "ci" {
			t.Fatalf("attach: %v called=%v reads=%v", err, called, fake.calls)
		}
	}
}

func TestDebugAttachRefusesUnusableOrUnobservedTargets(t *testing.T) {
	for _, change := range []func(*debugReaderFake){
		func(f *debugReaderFake) { f.job.Execution = nil },
		func(f *debugReaderFake) { f.job.Job.Status = "terminal" },
		func(f *debugReaderFake) {
			f.job.Execution.Kind = "runner"
			f.job.Execution.Pod = ""
			f.job.Execution.Namespace = ""
			f.job.Execution.Container = ""
		},
		func(f *debugReaderFake) {
			f.job.Execution.Kind = "local"
			f.job.Execution.Pod = ""
			f.job.Execution.Namespace = ""
			f.job.Execution.Container = ""
		},
		func(f *debugReaderFake) { f.job.Execution.Pod = "--context=other" },
		func(f *debugReaderFake) { f.job.Execution.Container = "" },
	} {
		fake := debugReaderFixture()
		change(fake)
		err := observeDebug(t.Context(), debugInvocation(t, "attach"), fake, io.Discard, func(context.Context, controllerwire.DebugExecution, io.Writer) error {
			t.Fatal("invalid target attached")
			return nil
		})
		if err == nil {
			t.Fatalf("invalid target accepted: %+v", fake.job)
		}
	}
	fake := debugReaderFixture()
	if err := observeDebug(t.Context(), debugInvocation(t, "attach", "--namespace=--context=other"), fake, io.Discard, func(context.Context, controllerwire.DebugExecution, io.Writer) error {
		t.Fatal("invalid namespace attached")
		return nil
	}); err == nil {
		t.Fatal("invalid namespace accepted")
	}
}

type debugBrokenWriter struct{ err error }

func (w debugBrokenWriter) Write([]byte) (int, error) { return 0, w.err }

func TestDebugReadCancellationAndOutputFailures(t *testing.T) {
	failure := errors.New("writer stopped")
	for _, verb := range []string{"env", "attach"} {
		fake := debugReaderFixture()
		inv := debugInvocation(t, verb)
		err := observeDebug(t.Context(), inv, fake, debugBrokenWriter{failure}, func(context.Context, controllerwire.DebugExecution, io.Writer) error {
			t.Fatal("output failure attached")
			return nil
		})
		if !errors.Is(err, failure) {
			t.Fatalf("output failure lost: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		fake.afterRead = cancel
		var out bytes.Buffer
		err = observeDebug(ctx, inv, fake, &out, func(context.Context, controllerwire.DebugExecution, io.Writer) error {
			t.Fatal("cancelled request attached")
			return nil
		})
		if !errors.Is(err, context.Canceled) || out.Len() != 0 {
			t.Fatalf("cancellation lost: %v %s", err, out.String())
		}
	}
}

func TestDebugAttachmentStreamsSelectedOutputFormat(t *testing.T) {
	for _, format := range []string{"json", "plain", "pretty"} {
		fake := debugReaderFixture()
		inv := debugInvocation(t, "attach")
		inv.Flags["output"] = []string{format}
		var out bytes.Buffer
		err := observeDebug(t.Context(), inv, fake, &out, func(_ context.Context, _ controllerwire.DebugExecution, stream io.Writer) error {
			_, err := io.WriteString(stream, "first\nsecond\n")
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if format != "json" {
			if out.String() != "first\nsecond\n" {
				t.Fatalf("child output changed: %q", out.String())
			}
			continue
		}
		decoder := json.NewDecoder(&out)
		var target controllerwire.DebugExecution
		var record textRecord
		if decoder.Decode(&target) != nil || target != *fake.job.Execution || decoder.Decode(&record) != nil || record.Kind != "debug_output" || record.Text != "first\nsecond\n" || decoder.Decode(new(any)) != io.EOF {
			t.Fatal("attachment stdout is not the declared NDJSON stream")
		}
	}
	failure := errors.New("output stopped")
	if _, err := (debugOutputWriter{out: debugBrokenWriter{failure}, format: "json"}).Write([]byte("child output")); !errors.Is(err, failure) {
		t.Fatalf("stream output failure lost: %v", err)
	}
}

func FuzzDebugAttachCannotChangeCommandStructure(f *testing.F) {
	f.Add("job-1", "ci", "runner")
	f.Add("--context=other", "ci", "runner")
	f.Add("pod:x", "-nother", "main")
	f.Fuzz(func(t *testing.T, pod, namespace, container string) {
		if len(pod)+len(namespace)+len(container) > 4096 {
			t.Skip()
		}
		fake := debugReaderFixture()
		fake.job.Execution.Pod, fake.job.Execution.Namespace, fake.job.Execution.Container = pod, namespace, container
		valid := fake.job.Execution.Validate() == nil
		attached := false
		err := observeDebug(t.Context(), debugInvocation(t, "attach"), fake, io.Discard, func(_ context.Context, target controllerwire.DebugExecution, _ io.Writer) error {
			attached = true
			if target != *fake.job.Execution || strings.ContainsAny(target.Pod+target.Namespace+target.Container, " /:\r\n\x00=") || strings.HasPrefix(target.Pod, "-") || strings.HasPrefix(target.Namespace, "-") || strings.HasPrefix(target.Container, "-") {
				t.Fatal("unsafe attachment reached command execution")
			}
			return nil
		})
		if attached != valid || (err == nil) != valid {
			t.Fatal("attachment and validation disagree")
		}
	})
}
