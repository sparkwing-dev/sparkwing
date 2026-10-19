//go:build unix && rewrite_integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestRunStartsLocalControllerBeforeSubmitting(t *testing.T) {
	probe, err := net.Listen("unix", filepath.Join(t.TempDir(), "probe.sock"))
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skipf("sandbox refuses local sockets: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	cli := buildSubmitCLI(t)
	cache, err := exec.CommandContext(t.Context(), "go", "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOMODCACHE", strings.TrimSpace(string(cache)))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	offlineStopDaemon(t, home)
	if status, err := wingdclient.QueryLocalController(t.Context(), home); !errors.Is(err, wingdclient.ErrNoDaemon) {
		t.Fatalf("expected no controller before run: %+v, %v", status, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, cli, "run", "example", "--sw-detached", "-o=json")
	command.Dir = root
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		log, logErr := wingd.LogPath(home)
		if logErr == nil {
			body, readErr := os.ReadFile(log)
			t.Logf("controller log: %s (%v)", body, readErr)
		}
		t.Fatalf("real run could not start and submit: %v\n%s\n%s", err, output, &stderr)
	}
	var receipt struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(output, &receipt); err != nil || receipt.RunID == "" {
		t.Fatalf("run did not submit: %s, %v", output, err)
	}
	status, err := wingdclient.QueryLocalController(ctx, home)
	if err != nil || !status.Running || status.PID <= 0 || status.Version == "" {
		t.Fatalf("run did not start a ready controller: %+v, %v", status, err)
	}
	read := exec.CommandContext(ctx, cli, "runs", "status", "--run", receipt.RunID, "-o=json")
	read.Dir = root
	output, err = read.Output()
	// runs status exits 3 for a run that has not finished, so the observed identity is the evidence.
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 3) {
		t.Fatalf("started controller did not retain submitted run: %v\n%s", err, output)
	}
	var observed struct {
		ID string `json:"id"`
	}
	if decodeErr := json.NewDecoder(strings.NewReader(string(output))).Decode(&observed); decodeErr != nil || observed.ID != receipt.RunID {
		t.Fatalf("started controller did not retain submitted run: %v, %v\n%s", err, decodeErr, output)
	}
}
