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
	if err == nil || !strings.Contains(err.Error(), "describe: no reply within 200ms") {
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

// A plan process that prints a secret default and then dies or stalls before
// describe answers leaves the host no way to know the secret, so its output is
// withheld whole and the sink is told so.
func TestAFailedDescribeWithholdsWhatThePlanProcessPrinted(t *testing.T) {
	const secret = "def-describe-secret"
	for name, tail := range map[string]string{"crash": "exit 3\n", "hang": "IFS= read -r line <&3\n/bin/sleep 3600\n"} {
		for _, via := range []string{"execute", "plan"} {
			t.Run(name+"/"+via, func(t *testing.T) {
				dir := t.TempDir()
				entry := filepath.Join(dir, "describe-fails")
				if err := os.WriteFile(entry, []byte("#!/bin/sh\necho plan sees def="+secret+"\n"+tail), 0o700); err != nil {
					t.Fatal(err)
				}
				sink := &planSinkText{masker: secrets.NewMasker()}
				cfg := SessionConfig{
					Entry: pipelineentry.Binary(entry), Dir: dir, Env: []string{"PATH=/usr/bin:/bin"},
					Grace: 50 * time.Millisecond, DescribeTimeout: 300 * time.Millisecond, Sink: sink,
				}
				var err error
				if via == "execute" {
					_, err = Execute(t.Context(), Config{
						Session: cfg, Pipeline: "demo", Run: nodeproto.RunContext{RunID: "describe-fails"},
						State: openStore(t), Logger: slog.New(slog.DiscardHandler),
					})
				} else {
					_, err = PlanDocument(t.Context(), cfg, PlanRequest{Pipeline: "demo", Run: nodeproto.RunContext{RunID: "describe-fails"}, Masker: sink.masker})
				}
				if err == nil {
					t.Fatal("describe succeeded")
				}
				if got := sink.String(); strings.Contains(got, secret) || !strings.Contains(got, "plan output withheld") {
					t.Fatalf("plan output = %q, want it withheld with a notice", got)
				}
			})
		}
	}
}

// What describe's plan code printed reaches the sink before release returns,
// so a session that closes right after planning cannot cut any of it off.
func TestReleaseDeliversTheHeldOutputBeforeReturning(t *testing.T) {
	sink := &planSinkText{masker: secrets.NewMasker()}
	cfg := SessionConfig{Sink: sink}
	gate := holdUntilDescribed(&cfg)
	for i := range 40 {
		cfg.Sink.Text(LevelInfo, fmt.Sprintf("line %d", i))
	}
	if got := sink.String(); got != "" {
		t.Fatalf("the sink got %q before describe answered", got)
	}
	gate.release()
	cfg.Sink.Text(LevelInfo, "after")
	gate.finish()
	if got := strings.Count(sink.String(), "\n"); got != 41 || !strings.HasSuffix(sink.String(), "line 39\nafter\n") {
		t.Fatalf("sink got %d lines:\n%s", got, sink.String())
	}
}
