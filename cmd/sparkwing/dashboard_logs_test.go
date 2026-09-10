//go:build !windows

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestServeLogsBoundsRecordsAndPreservesPlain(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, dashboardLogFile)
	var body strings.Builder
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&body, "line %02d\n", i)
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"json", "plain", "pretty"} {
		output := captureStdout(t, func() {
			if err := runSparkwing([]string{"serve", "logs", "--home", home, "-o", mode}); err != nil {
				t.Fatal(err)
			}
		})
		lines := strings.Split(strings.TrimSpace(output), "\n")
		if len(lines) != 40 {
			t.Fatalf("%s lines=%d", mode, len(lines))
		}
		if mode == "json" {
			records := decodeOutputRecords(t, []byte(output))
			if records[0]["line"] != "line 40" || records[39]["line"] != "line 79" || records[0]["kind"] != "log" {
				t.Fatal(records)
			}
		} else if lines[0] != "line 40" {
			t.Fatal(output)
		}
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	output := captureStdout(t, func() {
		if err := runDashboardLogs([]string{"--home", home, "-o", "json"}); err != nil {
			t.Fatal(err)
		}
	})
	if output != "" {
		t.Fatalf("empty log emitted %q", output)
	}
}

func TestServeLogsFollowEmitsAppendsAndCancels(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, dashboardLogFile)
	if err := os.WriteFile(path, []byte("first\npar"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := outputContractCommand(t, "serve", "logs", "--home", home, "--follow", "-o", "json")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	scanner := bufio.NewScanner(stdout)
	for _, want := range []string{"first", "partial"} {
		if want == "partial" {
			f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			if e != nil {
				t.Fatal(e)
			}
			_, e = f.WriteString("tial\n")
			f.Close()
			if e != nil {
				t.Fatal(e)
			}
		}
		if !scanner.Scan() {
			t.Fatalf("missing %s: %v", want, scanner.Err())
		}
		var record struct {
			Line string `json:"line"`
		}
		if err = json.Unmarshal(scanner.Bytes(), &record); err != nil || record.Line != want {
			t.Fatalf("log %s: %v", scanner.Text(), err)
		}
	}
	if err = command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err = command.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestServeLogTailUsesCapturedSnapshot(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.WriteString("before\npar"); err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("tial\nafter\n"); err != nil {
		t.Fatal(err)
	}
	lines, pending, err := dashboardLogTail(context.Background(), f, info.Size(), 0, 40, true)
	if err != nil || len(lines) != 1 || lines[0].Text != "before" || pending.Text != "par" {
		t.Fatalf("snapshot included later bytes or split partial: %+v %+v %v", lines, pending, err)
	}
	if _, err = f.WriteString(strings.Repeat("x", 2<<20)); err != nil {
		t.Fatal(err)
	}
	if tail := tailFileFrom(f.Name(), info.Size(), 40); len(tail) > 17*1024 || !strings.Contains(tail, "truncated") {
		t.Fatalf("startup failure tail was not bounded and marked: bytes=%d", len(tail))
	}
}

func TestServeLogsZeroSkipsHistoryAndHugeLineIsExplicit(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, dashboardLogFile)
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 40*1024)+"\nlast\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := captureStdout(t, func() {
		if err := runDashboardLogs([]string{"--home", home, "--limit", "0", "-o", "json"}); err != nil {
			t.Fatal(err)
		}
	})
	if output != "" {
		t.Fatalf("limit0 emitted history: %q", output)
	}
	output = captureStdout(t, func() {
		if err := runDashboardLogs([]string{"--home", home, "-o", "json"}); err != nil {
			t.Fatal(err)
		}
	})
	records := decodeOutputRecords(t, []byte(output))
	if len(records) != 2 || records[0]["truncated"] != true || len(records[0]["line"].(string)) != 16*1024 || records[1]["line"] != "last" {
		t.Fatalf("unexpected bounded log records: count=%d", len(records))
	}
}
