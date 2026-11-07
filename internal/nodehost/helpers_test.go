package nodehost

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/testshell"
)

func TestHostHelpersUsePipelineDirectoryAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("git", "-C", dir, "init", "--initial-branch=main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte("input"), 0o600); err != nil {
		t.Fatal(err)
	}
	routes, _, auth := serviceFixture(t)
	request := nodeproto.HelperRequest{Operation: "git.FilesetHash", Dir: dir, Env: os.Environ(), Args: json.RawMessage(`["."]`)}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response, err := callService(t, routes, auth, "/node/v1/helpers", string(body))
	if err != nil {
		t.Fatal(err)
	}
	var result nodeproto.HelperResponse
	if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var hash string
	if err = json.Unmarshal(result.Result, &hash); err != nil || len(hash) != 12 || result.Error != "" {
		t.Fatalf("helper result=%+v hash=%q error=%v", result, hash, err)
	}
	request.Dir = "relative"
	body, _ = json.Marshal(request)
	response, err = callService(t, routes, auth, "/node/v1/helpers", string(body))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Body.String(), "must be absolute") {
		t.Fatalf("relative helper accepted: %s", response.Body.String())
	}
}

func TestHostContainerHelperJoinsCleanupAndFiltersEngineEnvironment(t *testing.T) {
	dir := t.TempDir()
	testshell.Install(t, filepath.Join(dir, "docker"), `#!/bin/sh
printf '%s|%s\n' "$*" "${SPARKWING_API_TOKEN-unset}" >> "$HELPER_RECORD"
case "$1" in
 start) printf 'container output';;
 login) cat >&2; exit 1;;
esac
`)
	record := filepath.Join(dir, "commands")
	environment := append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "HELPER_RECORD="+record, "SPARKWING_API_TOKEN=engine-private")
	routes, service, auth := serviceFixture(t)
	service.Cleanups = &ProcessCleanups{}
	invoke := func(operation, args string) nodeproto.HelperResponse {
		t.Helper()
		body, _ := json.Marshal(nodeproto.HelperRequest{Operation: operation, Dir: dir, Env: environment, Args: json.RawMessage(args)})
		out, err := callService(t, routes, auth, "/node/v1/helpers", string(body))
		if err != nil {
			t.Fatal(err)
		}
		var response nodeproto.HelperResponse
		if err = json.Unmarshal(out.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := invoke("docker.Run", `[{"image":"fixture"}]`)
	if response.Error != "" || !strings.Contains(string(response.Result), "container output") {
		t.Fatalf("container result=%+v", response)
	}
	service.Cleanups.mu.Lock()
	remaining := len(service.Cleanups.list.items)
	service.Cleanups.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("cleanup remains registered: %d", remaining)
	}
	commands, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(commands), "rm -f sparkwing-run-") || strings.Contains(string(commands), "engine-private") {
		t.Fatalf("cleanup/environment: %s", commands)
	}
	secret := "helper-private-password"
	response = invoke("docker.Login", `["registry","user","helper-private-password"]`)
	if response.Error == "" || strings.Contains(response.Error, secret) {
		t.Fatalf("login failure was not masked: %+v", response)
	}
	commands, err = os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commands), secret) {
		t.Fatal("password entered command arguments")
	}
}
