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
			Entry: entry, Dir: dir, Env: []string{"PATH=/usr/bin:/bin"},
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
