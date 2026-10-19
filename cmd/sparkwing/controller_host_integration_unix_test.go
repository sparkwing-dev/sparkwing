//go:build unix && rewrite_integration

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerhost"
)

func TestSelfHostedControllerHostsAnUnselectedTriggerWithoutRunners(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skipf("controller hosting requires loopback sockets: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	cli, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repo := runAnywhereRepo(t)
	endpoint := "http://" + addr
	state, server, bearer := burstIntakeController(t, endpoint)
	config := filepath.Join(os.Getenv("SPARKWING_HOME"), "config.yaml")
	if err := os.WriteFile(config, fmt.Appendf(nil, "profiles:\n  self:\n    controller:\n      url: %q\n      token: %q\n", endpoint, bearer), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", config)
	offlineStopDaemon(t, os.Getenv("SPARKWING_HOME"))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	served := make(chan error, 1)
	go func() {
		served <- controllerhost.Serve(ctx, server, addr, controllerhost.Config{Home: t.TempDir(), Version: installedVersion()})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Error(err)
		}
	})
	triggerCtx, stop := context.WithTimeout(ctx, 3*time.Minute)
	defer stop()
	command := exec.CommandContext(triggerCtx, cli, "pipeline", "trigger", "anywhere", "--profile", "self", "--working-tree", "-o=json")
	command.Dir = repo
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "anywhere-generated-ran") || !strings.Contains(string(output), "anywhere-untyped-generated-ran") {
		t.Fatalf("self-hosted trigger: %v\n%s", err, output)
	}
	rows, err := state.DB().QueryContext(ctx, `SELECT b.unit_kind,b.metered,c.subject_id FROM billing_awards b JOIN capabilities c ON c.prefix=b.capability_prefix`)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for rows.Next() {
		var kind, subject string
		var metered int
		if err := rows.Scan(&kind, &metered, &subject); err != nil {
			t.Fatal(err)
		}
		if metered != 0 || subject != "controller-host" {
			t.Fatalf("%s award went to %q, metered=%d", kind, subject, metered)
		}
		kinds[kind] = true
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil || !kinds["plan"] || !kinds["eval"] || !kinds["node"] {
		t.Fatalf("the controller host must take the plan, evaluation and job units: %v %v", kinds, err)
	}
	var observed int
	if err := state.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM capabilities WHERE subject_id='controller-host' AND parent_id IS NULL AND claim_observation_json IS NOT NULL`).Scan(&observed); err != nil || observed != 1 {
		t.Fatalf("the controller host's polls must be ranking observations: %d %v", observed, err)
	}
	var runners, attempts, hosted int
	if err := state.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM capabilities WHERE subject_kind='machine'`).Scan(&runners); err != nil || runners != 0 {
		t.Fatalf("the controller must have no registered runner: %d %v", runners, err)
	}
	if err := state.DB().QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(executor_name='controller-host'),0) FROM node_attempts`).Scan(&attempts, &hosted); err != nil || attempts < 4 || hosted != attempts {
		t.Fatalf("the controller host must run every job, generated ones included: %d of %d %v", hosted, attempts, err)
	}
}
