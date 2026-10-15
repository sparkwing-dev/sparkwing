//go:build windows

package procgroup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestJobSuspendedStartProbe(t *testing.T) {
	if marker := os.Getenv("SPARKWING_TEST_JOB_START_MARKER"); marker != "" {
		if err := os.WriteFile(marker, []byte("started"), 0o600); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
}

func FuzzJobSuspendedStartRefusesUnexpectedResume(f *testing.F) {
	for _, previous := range []uint32{0, 1, 2, ^uint32(0)} {
		f.Add(previous, false)
		f.Add(previous, true)
	}
	f.Fuzz(func(t *testing.T, previous uint32, failed bool) {
		job, err := NewJob("")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := job.Close(); err != nil {
				t.Error(err)
			}
		})
		marker := filepath.Join(t.TempDir(), "body-started")
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestJobSuspendedStartProbe$")
		cmd.Env = append(os.Environ(), "SPARKWING_TEST_JOB_START_MARKER="+marker)
		t.Cleanup(func() {
			if cmd.Process != nil && cmd.ProcessState == nil {
				if err := job.Terminate(); err != nil {
					t.Error(err)
				}
				_ = cmd.Wait()
			}
		})
		err = job.start(cmd, func(thread windows.Handle) (uint32, error) {
			if failed {
				return previous, errors.New("resume refused")
			}
			if previous == 1 {
				return windows.ResumeThread(thread)
			}
			return previous, nil
		})
		valid := previous == 1 && !failed
		if (err == nil) != valid {
			t.Fatalf("resume count %d, failed=%t: %v", previous, failed, err)
		}
		waitErr := cmd.Wait()
		if valid && waitErr != nil || cmd.ProcessState == nil || ctx.Err() != nil {
			t.Fatalf("child was not reaped: state=%v, wait=%v, context=%v", cmd.ProcessState, waitErr, ctx.Err())
		}
		_, markerErr := os.Stat(marker)
		if valid && markerErr != nil || !valid && !os.IsNotExist(markerErr) {
			t.Fatalf("body execution differs from accepted start: %v", markerErr)
		}
	})
}
