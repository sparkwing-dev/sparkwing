//go:build unix && rewrite_integration

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestBurstPublicRunBuildsEditedSourceAndStreamsMaskedSecrets(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skipf("native burst requires loopback and local-controller sockets: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	cli := buildSubmitCLI(t)
	t.Setenv("GOMODCACHE", offlineGoEnv(t, "GOMODCACHE"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("TOKEN", burstIntakeSecret)
	repo := burstIntakeRepository(t)
	endpoint := "http://" + listener.Addr().String()
	_, controller, bearer := burstIntakeController(t, endpoint)
	server := httptest.NewUnstartedServer(controller.Handler())
	if err := server.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	config := filepath.Join(t.TempDir(), "config.yaml")
	contents := fmt.Sprintf("profiles:\n  cloud:\n    controller:\n      url: %q\n      token: %q\nrun:\n  routing:\n    route: cloud\n    profile: cloud\n    local_secrets: [TOKEN]\n", endpoint, bearer)
	if err := os.WriteFile(config, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", config)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	offlineStopDaemon(t, os.Getenv("SPARKWING_HOME"))
	invoke := func(args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(ctx, cli, args...)
		command.Dir = repo
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("public burst CLI %v: %v\n%s", args, err, output)
		}
		return output
	}
	var overflow controllerwire.OverflowControl
	if err := json.Unmarshal(invoke("overflow", "show", "--profile", "cloud", "-o=json"), &overflow); err != nil || overflow.MonthlyMinutes != nil {
		t.Fatalf("new requester has an overflow cap: %+v %v", overflow, err)
	}
	invoke("overflow", "set", "--profile", "cloud", "--minutes", "0", "-o=json")
	handleFile := filepath.Join(t.TempDir(), "run.json")
	output := invoke("run", "burst", "--sw-run-handle-file", handleFile, "-o=json")
	if !strings.Contains(string(output), "burst-edited-ok secret=*** encoded=***") || strings.Contains(string(output), burstIntakeSecret) || strings.Contains(string(output), base64.StdEncoding.EncodeToString([]byte(burstIntakeSecret))) {
		t.Fatalf("native burst lost masked terminal output: %s", output)
	}
	var handle orchestrator.RunHandle
	body, err := os.ReadFile(handleFile)
	if err != nil || json.Unmarshal(body, &handle) != nil || handle.RunID == "" {
		t.Fatalf("public burst lost admitted run identity: %s %v", body, err)
	}
	member, err := client.NewEngineClient(client.EngineOptions{URL: endpoint, Bearer: func(context.Context) (string, error) { return bearer, nil }})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := member.ReadRun(ctx, handle.RunID)
	if err != nil || detail.Status != "success" {
		t.Fatalf("Cloud owner did not retain successful native execution: %+v %v", detail, err)
	}
	if local := invoke("runs", "list", "-o=json"); strings.Contains(string(local), handle.RunID) {
		t.Fatalf("burst copied ownership into the local controller: %s", local)
	}
}
