package nodehost_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func cleanupNode(t *testing.T, s *nodehost.Server) (string, func()) {
	t.Helper()
	bearer, revoke, err := s.Admit(nodehost.Node{
		RunID: "run-1", NodeID: "deploy", Resolver: &fakeResolver{}, Masker: secrets.NewMasker(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return bearer, revoke
}

func send(t *testing.T, s *nodehost.Server, method, path, bearer string, body []byte) reply {
	t.Helper()
	req, err := http.NewRequest(method, s.URL()+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set(authwire.NodeProtocolHeader, authwire.NodeProtocolVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := reply{status: resp.StatusCode}
	if resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(&out.body); err != nil {
			t.Fatalf("%s %s: decode reply: %v", method, path, err)
		}
	}
	return out
}

func register(t *testing.T, s *nodehost.Server, bearer string, argv []string, description string) reply {
	t.Helper()
	body, err := json.Marshal(map[string]any{"argv": argv, "description": description})
	if err != nil {
		t.Fatal(err)
	}
	return send(t, s, http.MethodPost, "/node/v1/cleanups", bearer, body)
}

func mustRegister(t *testing.T, s *nodehost.Server, bearer string, argv ...string) string {
	t.Helper()
	got := register(t, s, bearer, argv, "")
	id, _ := got.body["id"].(string)
	if got.status != http.StatusCreated || id == "" {
		t.Fatalf("register %v: status %d body %v, want 201 with an id", argv, got.status, got.body)
	}
	return id
}

func release(t *testing.T, s *nodehost.Server, bearer, id string) int {
	t.Helper()
	return send(t, s, http.MethodDelete, "/node/v1/cleanups/"+id, bearer, nil).status
}

func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the cleanups under test are POSIX shell commands")
	}
}

func shellEnv(extra ...string) []string {
	return append([]string{"PATH=" + os.Getenv("PATH")}, extra...)
}

func TestCleanupsRunNewestFirstSkippingReleasedOnes(t *testing.T) {
	requireUnix(t)
	s, _ := start(t)
	bearer, revoke := cleanupNode(t, s)
	dir := t.TempDir()
	for _, n := range []string{"first", "second", "third"} {
		id := mustRegister(t, s, bearer, "sh", "-c", "echo "+n+" >> order")
		if n == "second" {
			if status := release(t, s, bearer, id); status != http.StatusNoContent {
				t.Fatalf("release: status %d, want 204", status)
			}
			if status := release(t, s, bearer, id); status != http.StatusNotFound {
				t.Fatalf("second release: status %d, want 404", status)
			}
		}
	}
	revoke()
	if err := s.RunCleanups(context.Background(), bearer, nodehost.CleanupScope{Dir: dir, Env: shellEnv()}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "order"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "third\nfirst\n" {
		t.Fatalf("cleanups ran as %q, want third then first", got)
	}
	if err := s.RunCleanups(context.Background(), bearer, nodehost.CleanupScope{Dir: dir, Env: shellEnv()}); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(filepath.Join(dir, "order")); !bytes.Equal(again, got) {
		t.Fatalf("a second RunCleanups ran them again: %q", again)
	}
}

func TestCleanupRunsWithTheNodesEnvironmentNotTheHosts(t *testing.T) {
	requireUnix(t)
	t.Setenv("HOST_ONLY_CREDENTIAL", "host-secret")
	s, _ := start(t)
	bearer, revoke := cleanupNode(t, s)
	mustRegister(t, s, bearer, "sh", "-c", `echo "node=$NODE_VAR host=$HOST_ONLY_CREDENTIAL token=$SPARKWING_NODE_TOKEN dir=$(pwd)"`)
	revoke()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var log syncBuffer
	scope := nodehost.CleanupScope{Dir: dir, Log: &log, Env: shellEnv("NODE_VAR=node-value", "SPARKWING_NODE_TOKEN="+bearer)}
	if err := s.RunCleanups(context.Background(), bearer, scope); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("node=node-value host= token= dir=%s\n", dir)
	if !strings.Contains(log.String(), want) {
		t.Fatalf("cleanup output:\n%s\nwant a line %q", log.String(), want)
	}
}

func TestCleanupResolvesItsProgramOnTheNodesPath(t *testing.T) {
	requireUnix(t)
	s, _ := start(t)
	bearer, revoke := cleanupNode(t, s)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "node-tool"), []byte("#!/bin/sh\necho node-tool ran\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustRegister(t, s, bearer, "node-tool")
	revoke()
	var log syncBuffer
	scope := nodehost.CleanupScope{Dir: t.TempDir(), Log: &log, Env: []string{"PATH=" + bin}}
	if err := s.RunCleanups(context.Background(), bearer, scope); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "node-tool ran") {
		t.Fatalf("cleanup output:\n%s\nwant node-tool found on the node's PATH", log.String())
	}
}

func TestCleanupFailuresNameOnlyTheProgramAndTheRestStillRun(t *testing.T) {
	requireUnix(t)
	s, logs := start(t)
	bearer, revoke := cleanupNode(t, s)
	dir := t.TempDir()
	mustRegister(t, s, bearer, "sh", "-c", "touch ran")
	mustRegister(t, s, bearer, "no-such-program-xyz", "--token=s3cret-arg")
	mustRegister(t, s, bearer, "sh", "-c", "exit 3", "s3cret-arg")
	revoke()
	var log syncBuffer
	if err := s.RunCleanups(context.Background(), bearer, nodehost.CleanupScope{Dir: dir, Log: &log, Env: shellEnv()}); err != nil {
		t.Fatal(err)
	}
	out := log.String()
	for _, want := range []string{"cleanup sh failed: exit status 3", "cleanup no-such-program-xyz failed: not found"} {
		if !strings.Contains(out, want) {
			t.Fatalf("cleanup log:\n%s\nwant %q", out, want)
		}
	}
	if strings.Contains(out, "s3cret-arg") || strings.Contains(logs.String(), "s3cret-arg") {
		t.Fatalf("logs carry a cleanup argument:\n%s\n%s", out, logs.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); err != nil {
		t.Fatalf("the oldest cleanup did not run after two failures: %v", err)
	}
}

func TestCleanupIsKilledAtItsDeadline(t *testing.T) {
	requireUnix(t)
	s, _ := start(t)
	bearer, revoke := cleanupNode(t, s)
	mustRegister(t, s, bearer, "sh", "-c", "exec sleep 60", "s3cret-arg")
	revoke()
	var log syncBuffer
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := s.RunCleanups(ctx, bearer, nodehost.CleanupScope{Dir: t.TempDir(), Log: &log, Env: shellEnv()}); err != nil {
		t.Fatal(err)
	}
	if out := log.String(); !strings.Contains(out, "cleanup sh failed: timed out") || strings.Contains(out, "s3cret-arg") {
		t.Fatalf("cleanup log:\n%s\nwant a timeout naming only sh", out)
	}
}

func TestCleanupsAreConfinedToTheirNode(t *testing.T) {
	requireUnix(t)
	s, _ := start(t)
	bearerA, revokeA := cleanupNode(t, s)
	bearerB, revokeB := cleanupNode(t, s)
	dir := t.TempDir()
	id := mustRegister(t, s, bearerA, "sh", "-c", "touch a-ran")
	if got := send(t, s, http.MethodDelete, "/node/v1/cleanups/"+id, bearerB, nil); got.status != http.StatusNotFound || got.body["error"] != "unknown_cleanup" {
		t.Fatalf("node B released node A's cleanup: status %d body %v, want 404 unknown_cleanup", got.status, got.body)
	}
	mustRegister(t, s, bearerB, "sh", "-c", "touch b-ran")
	revokeA()
	if err := s.RunCleanups(context.Background(), bearerA, nodehost.CleanupScope{Dir: dir, Env: shellEnv()}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a-ran")); err != nil {
		t.Fatalf("node A's cleanup did not run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b-ran")); err == nil {
		t.Fatal("node A's exit ran node B's cleanup")
	}
	revokeB()
}

func TestRunCleanupsRefusesALiveBearer(t *testing.T) {
	requireUnix(t)
	s, _ := start(t)
	bearer, revoke := cleanupNode(t, s)
	dir := t.TempDir()
	mustRegister(t, s, bearer, "sh", "-c", "touch ran")
	if err := s.RunCleanups(context.Background(), bearer, nodehost.CleanupScope{Dir: dir, Env: shellEnv()}); err == nil {
		t.Fatal("RunCleanups ran while the node's bearer was live")
	}
	revoke()
	if got := register(t, s, bearer, []string{"true"}, ""); got.status != http.StatusUnauthorized {
		t.Fatalf("register after revoke: status %d, want 401", got.status)
	}
	if err := s.RunCleanups(context.Background(), bearer, nodehost.CleanupScope{Dir: dir, Env: shellEnv()}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); err != nil {
		t.Fatalf("the refused run lost the cleanup: %v", err)
	}
}

func TestCleanupRegistrationLimits(t *testing.T) {
	s, _ := start(t)
	bearer, _ := cleanupNode(t, s)
	for name, body := range map[string][]byte{
		"empty argv":        []byte(`{"argv":[]}`),
		"empty program":     []byte(`{"argv":[""]}`),
		"no argv":           []byte(`{}`),
		"NUL in argv":       []byte(`{"argv":["rm","a\u0000b"]}`),
		"not json":          []byte(`argv=rm`),
		"65 arguments":      []byte(`{"argv":["x"` + strings.Repeat(`,"a"`, 64) + `]}`),
		"long description":  []byte(`{"argv":["x"],"description":"` + strings.Repeat("d", 257) + `"}`),
		"body over 16 KiB":  []byte(`{"argv":["x","` + strings.Repeat("a", 16<<10) + `"]}`),
		"argv not a string": []byte(`{"argv":[1]}`),
	} {
		if got := send(t, s, http.MethodPost, "/node/v1/cleanups", bearer, body); got.status != http.StatusBadRequest || got.body["error"] != "bad_request" {
			t.Fatalf("%s: status %d body %v, want 400 bad_request", name, got.status, got.body)
		}
	}
	if got := register(t, s, bearer, append([]string{"x"}, make([]string, 63)...), strings.Repeat("d", 256)); got.status != http.StatusCreated {
		t.Fatalf("64 arguments and a 256-byte description: status %d body %v, want 201", got.status, got.body)
	}
	var first string
	for i := 1; i < 16; i++ {
		id := mustRegister(t, s, bearer, "true")
		if first == "" {
			first = id
		}
	}
	if got := register(t, s, bearer, []string{"true"}, ""); got.status != http.StatusBadRequest {
		t.Fatalf("17th cleanup: status %d body %v, want 400", got.status, got.body)
	}
	if status := release(t, s, bearer, first); status != http.StatusNoContent {
		t.Fatalf("release: status %d", status)
	}
	mustRegister(t, s, bearer, "true")
}
