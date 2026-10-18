package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func dashboardLogFixture(t *testing.T, body string) (string, dashboardServices) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "controller.log")
	if err := fssecure.WriteFile(path, []byte(body)); err != nil {
		t.Fatal(err)
	}
	return path, dashboardServices{openLog: func(context.Context) (*os.File, error) { return fssecure.OpenPrivateConfig(path) }}
}

func TestDashboardLogsBoundsRecordsAndPreservesPlain(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&body, "line %02d\n", i)
	}
	_, services := dashboardLogFixture(t, body.String())
	for _, mode := range []string{"json", "plain", "pretty"} {
		var output bytes.Buffer
		err := runDashboardCommandArgs(t.Context(), []string{"logs", "-o", mode}, &output, "linux", func(context.Context) (dashboardServices, error) { return services, nil })
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(output.String()), "\n")
		if len(lines) != 40 {
			t.Fatalf("%s lines=%d", mode, len(lines))
		}
		if mode == "json" {
			records := decodeOutputRecords(t, output.Bytes())
			if records[0]["line"] != "line 40" || records[39]["line"] != "line 79" || records[0]["kind"] != "log" {
				t.Fatal(records)
			}
		} else if lines[0] != "line 40" {
			t.Fatal(output.String())
		}
	}
}

type dashboardLogObserver func([]byte) (int, error)

func (f dashboardLogObserver) Write(p []byte) (int, error) { return f(p) }

func TestDashboardLogsFollowEmitsAppendsAndCancels(t *testing.T) {
	path, services := dashboardLogFixture(t, "first\npar")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var output bytes.Buffer
	lines := 0
	observer := dashboardLogObserver(func(p []byte) (int, error) {
		lines++
		if lines == 1 {
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return 0, err
			}
			_, writeErr := file.WriteString("tial\n")
			if err := errors.Join(writeErr, file.Close()); err != nil {
				return 0, err
			}
		} else {
			cancel()
		}
		return output.Write(p)
	})
	err := runDashboardCommandArgs(ctx, []string{"logs", "-f", "-o", "plain"}, observer, "linux", func(context.Context) (dashboardServices, error) { return services, nil })
	if err != nil || output.String() != "first\npartial\n" {
		t.Fatalf("output=%q err=%v", &output, err)
	}
}

func TestDashboardLogTailUsesCapturedSnapshot(t *testing.T) {
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
	lines, pending, err := dashboardLogTail(t.Context(), f, info.Size(), 40, true)
	if err != nil || len(lines) != 1 || lines[0].Text != "before" || pending.Text != "par" {
		t.Fatalf("snapshot: %+v %+v %v", lines, pending, err)
	}
}

func TestDashboardLogsZeroSkipsHistoryAndHugeLineIsExplicit(t *testing.T) {
	_, services := dashboardLogFixture(t, strings.Repeat("x", 40*1024)+"\nlast\n")
	for _, tc := range []struct {
		tail  string
		count int
	}{{"0", 0}, {"40", 2}} {
		var output bytes.Buffer
		err := runDashboardCommandArgs(t.Context(), []string{"logs", "--tail", tc.tail, "-o", "json"}, &output, "linux", func(context.Context) (dashboardServices, error) { return services, nil })
		if err != nil {
			t.Fatal(err)
		}
		records := decodeOutputRecords(t, output.Bytes())
		if len(records) != tc.count {
			t.Fatalf("records=%d", len(records))
		}
		if tc.count > 0 && (records[0]["truncated"] != true || len(records[0]["line"].(string)) != 16*1024 || records[1]["line"] != "last") {
			t.Fatalf("records=%v", records)
		}
	}
}

func TestDashboardLogsFailOnMissingSourceOrOutput(t *testing.T) {
	_, services := dashboardLogFixture(t, "one\n")
	failure := errors.New("output failed")
	err := runDashboardCommandArgs(t.Context(), []string{"logs"}, dashboardLogObserver(func([]byte) (int, error) { return 0, failure }), "linux", func(context.Context) (dashboardServices, error) { return services, nil })
	if !errors.Is(err, failure) {
		t.Fatalf("output error lost: %v", err)
	}
	err = runDashboardCommandArgs(t.Context(), []string{"logs"}, io.Discard, "linux", func(context.Context) (dashboardServices, error) { return dashboardServices{}, nil })
	var unavailable *apierr.Error
	if !errors.As(err, &unavailable) || unavailable.Code != apierr.Unavailable {
		t.Fatalf("missing source accepted: %v", err)
	}
	services.openLog = func(context.Context) (*os.File, error) { return nil, os.ErrNotExist }
	err = runDashboardCommandArgs(t.Context(), []string{"logs"}, io.Discard, "linux", func(context.Context) (dashboardServices, error) { return services, nil })
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing bytes silently accepted: %v", err)
	}
}
