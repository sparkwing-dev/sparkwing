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

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestAStalledDescribeEndsTheRunBeforeItIsRecorded(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "stall-describe")
	script := fmt.Sprintf("#!/bin/sh\ntrap '' TERM\n/bin/sleep 3600 &\necho $! > %s/child\nIFS= read -r line <&3\nwait\n", dir)
	if err := os.WriteFile(entry, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, err := Execute(ctx, Config{
		Session: SessionConfig{
			Entry: pipelineentry.Binary(entry), Dir: dir, Env: []string{"PATH=/usr/bin:/bin"},
			Grace: 50 * time.Millisecond, DescribeTimeout: 200 * time.Millisecond,
		},
		Pipeline: "demo", Run: nodeproto.RunContext{RunID: "stalled-describe"},
		State: st, Logger: slog.New(slog.DiscardHandler),
	})
	if err == nil || !strings.Contains(err.Error(), "describe failed: no reply within 200ms") {
		t.Fatalf("err = %v, want the describe deadline", err)
	}
	if _, err := st.GetRun(t.Context(), "stalled-describe"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a run whose describe stalled was recorded: %v", err)
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
	if err := os.WriteFile(entry, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := SessionConfig{
		Entry: pipelineentry.Binary(entry), Dir: dir, Env: []string{"PATH=/usr/bin:/bin"},
		Grace: 50 * time.Millisecond, DescribeTimeout: 300 * time.Millisecond, Sink: sink,
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var err error
	if via == "execute" {
		_, err = Execute(ctx, Config{
			Session: cfg, Pipeline: "demo", Run: nodeproto.RunContext{RunID: "describe-fails"},
			State: openStore(t), Logger: slog.New(slog.DiscardHandler), Attempts: &Attempts{Masker: secrets.NewMasker()},
		})
	} else {
		_, err = PlanDocument(ctx, cfg, PlanRequest{Pipeline: "demo", Run: nodeproto.RunContext{RunID: "describe-fails"}, Masker: secrets.NewMasker()})
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
			var perr *nodeproto.Error
			if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "describe failed: plan_failed") || !errors.As(err, &perr) {
				t.Fatalf("err = %v, want its class without its text", err)
			}
		})
	}
}

type stalledSink struct{ unblock chan struct{} }

func (s stalledSink) Text(string, string)        { <-s.unblock }
func (s stalledSink) Record(nodeproto.LogRecord) { <-s.unblock }

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
	sink := &planSinkText{masker: secrets.NewMasker()}
	describeFails(t, "i=0; while [ $i -lt 140000 ]; do echo; i=$((i+1)); done\nexit 3\n", "plan", sink)
	if got := sink.String(); !strings.Contains(got, "(140000 lines)") {
		t.Fatalf("plan output = %q, want a notice counting all 140000 lines", got[max(0, len(got)-200):])
	}
}
