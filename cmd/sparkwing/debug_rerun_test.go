package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
)

// TestBuildRerunEnv_OverlayWins asserts the snapshot env beats the
// caller's base env on key collisions, while non-conflicting base
// keys are preserved (so the shell still has PATH etc.).
func TestBuildRerunEnv_OverlayWins(t *testing.T) {
	envJSON, _ := json.Marshal(map[string]string{
		"SPARKWING_RUN_ID": "run-abc",
		"SHARED":           "from-snapshot",
	})
	snap := &store.NodeDispatch{EnvJSON: envJSON}

	got, err := BuildRerunEnv(snap, "/tmp/refs", []string{
		"PATH=/usr/bin",
		"SHARED=from-base",
	})
	if err != nil {
		t.Fatalf("BuildRerunEnv: %v", err)
	}
	m := envListToMap(got)
	if m["PATH"] != "/usr/bin" {
		t.Fatalf("PATH should pass through: %v", m)
	}
	if m["SHARED"] != "from-snapshot" {
		t.Fatalf("snapshot must beat base on conflict: SHARED=%q", m["SHARED"])
	}
	if m["SPARKWING_RUN_ID"] != "run-abc" {
		t.Fatalf("snapshot key missing: %v", m)
	}
	if m["SPARKWING_RERUN"] != "1" {
		t.Fatalf("rerun marker missing: %v", m)
	}
	if m["SPARKWING_RERUN_REFS_DIR"] != "/tmp/refs" {
		t.Fatalf("refs dir missing: %v", m)
	}
}

// TestBuildRerunEnv_OmitRefsDir keeps the env clean when no refs dir
// is in play (cluster mode does not materialize refs).
func TestBuildRerunEnv_OmitRefsDir(t *testing.T) {
	snap := &store.NodeDispatch{EnvJSON: []byte(`{}`)}
	got, err := BuildRerunEnv(snap, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	m := envListToMap(got)
	if _, ok := m["SPARKWING_RERUN_REFS_DIR"]; ok {
		t.Fatalf("refs dir should be absent: %v", m)
	}
	if m["SPARKWING_RERUN"] != "1" {
		t.Fatalf("rerun marker missing: %v", m)
	}
}

// TestBuildRerunEnv_EmptyEnvJSON handles a snapshot whose env_json
// blob is nil (older row, or a write that lost the env). The base
// env still flows through; the rerun marker is still set.
func TestBuildRerunEnv_EmptyEnvJSON(t *testing.T) {
	got, err := BuildRerunEnv(&store.NodeDispatch{}, "", []string{"FOO=bar"})
	if err != nil {
		t.Fatalf("BuildRerunEnv: %v", err)
	}
	m := envListToMap(got)
	if m["FOO"] != "bar" {
		t.Fatalf("base env dropped: %v", m)
	}
	if m["SPARKWING_RERUN"] != "1" {
		t.Fatalf("rerun marker missing: %v", m)
	}
}

// TestBuildRerunEnv_BadJSON surfaces a parse error rather than
// silently returning an empty map.
func TestBuildRerunEnv_BadJSON(t *testing.T) {
	snap := &store.NodeDispatch{EnvJSON: []byte("not-json")}
	if _, err := BuildRerunEnv(snap, "", nil); err == nil {
		t.Fatalf("expected error on bad JSON")
	}
}

// TestMaterializeLocalRefs writes one file per dep with the upstream
// node's output bytes. Missing deps are warned-about, not fatal.
func TestMaterializeLocalRefs(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID:        "run-1",
		Pipeline:  "p",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, n := range []store.Node{
		{RunID: "run-1", NodeID: "build", Status: "done"},
		{RunID: "run-1", NodeID: "fetch", Status: "done"},
	} {
		if err := st.CreateNode(ctx, n); err != nil {
			t.Fatalf("CreateNode: %v", err)
		}
		if err := st.FinishNode(ctx, n.RunID, n.NodeID, "success", "", []byte(`{"src":"`+n.NodeID+`"}`)); err != nil {
			t.Fatalf("FinishNode: %v", err)
		}
	}

	refsDir := filepath.Join(dir, "refs")
	if err := materializeLocalRefs(ctx, st, refsDir, "run-1", []string{"build", "fetch", "missing"}); err != nil {
		t.Fatalf("materializeLocalRefs: %v", err)
	}

	for _, want := range []string{"build.json", "fetch.json"} {
		body, err := os.ReadFile(filepath.Join(refsDir, want))
		if err != nil {
			t.Fatalf("ref file %s: %v", want, err)
		}
		if !strings.Contains(string(body), `"src"`) {
			t.Fatalf("ref body wrong: %s", string(body))
		}
	}
	if _, err := os.Stat(filepath.Join(refsDir, "missing.json")); err == nil {
		t.Fatalf("missing dep should not have produced a ref file")
	}
}

// TestPrintRerunBanner sanity: the banner mentions the key context
// (run, node, seq, workdir, error). Layout-only assertions; format
// changes require updating this test.
func TestPrintRerunBanner(t *testing.T) {
	snap := &store.NodeDispatch{
		RunID: "run-X", NodeID: "deploy", Seq: 2,
		Workdir: "/repo", CodeVersion: "abc1234",
		DispatchedAt: time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC),
	}
	node := &store.Node{
		RunID: "run-X", NodeID: "deploy",
		Status: "failed",
		Error:  "deploy failed: argocd timeout\nplus more lines",
	}
	var buf bytes.Buffer
	printRerunBanner(&buf, snap, node, "/tmp/refs")

	out := buf.String()
	for _, want := range []string{"run-X", "deploy", "seq=2", "/repo", "abc1234", "/tmp/refs", "argocd timeout"} {
		if !strings.Contains(out, want) {
			t.Errorf("banner missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "argocd timeout\nplus more lines") {
		t.Errorf("banner should single-line the error\n%s", out)
	}
}

// TestPodName produces a DNS-label-safe pod name. Length, charset,
// uniqueness across calls.
func TestPodName(t *testing.T) {
	a := podName("run-1", "build")
	b := podName("run-1", "build")
	if a == b {
		t.Errorf("podName should be unique across calls: %s == %s", a, b)
	}
	if !strings.HasPrefix(a, "sparkwing-rerun-") {
		t.Errorf("missing prefix: %s", a)
	}
	if len(a) > 63 {
		t.Errorf("DNS label too long: %d chars", len(a))
	}
	for _, r := range a {
		if !(r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			t.Errorf("disallowed char %q in %s", r, a)
		}
	}
}

func envListToMap(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		out[kv[:i]] = kv[i+1:]
	}
	return out
}
