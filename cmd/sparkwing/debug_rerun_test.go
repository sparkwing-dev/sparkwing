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

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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

func TestBuildRerunEnv_BadJSON(t *testing.T) {
	snap := &store.NodeDispatch{EnvJSON: []byte("not-json")}
	if _, err := BuildRerunEnv(snap, "", nil); err == nil {
		t.Fatalf("expected error on bad JSON")
	}
}

func TestMaterializeLocalRefs(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

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

func TestRerunPodManifest_EnvOffArgv(t *testing.T) {
	raw, err := rerunPodManifest("sparkwing-rerun-abc", "ghcr.io/me/runner:v1", "run-X", map[string]string{
		"SPARKWING_RUN_ID": "run-X",
		"SPARKWING_RERUN":  "1",
	})
	if err != nil {
		t.Fatalf("rerunPodManifest: %v", err)
	}
	var pod struct {
		Metadata struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			RestartPolicy         string `json:"restartPolicy"`
			ActiveDeadlineSeconds int    `json:"activeDeadlineSeconds"`
			Containers            []struct {
				Image     string `json:"image"`
				Stdin     bool   `json:"stdin"`
				StdinOnce bool   `json:"stdinOnce"`
				TTY       bool   `json:"tty"`
				Env       []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"env"`
			} `json:"containers"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &pod); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if pod.Metadata.Name != "sparkwing-rerun-abc" {
		t.Fatalf("pod name: %q", pod.Metadata.Name)
	}
	if pod.Metadata.Labels["sparkwing.dev/rerun-of-run"] != "run-X" {
		t.Fatalf("labels: %v", pod.Metadata.Labels)
	}
	if pod.Spec.RestartPolicy != "Never" || pod.Spec.ActiveDeadlineSeconds != rerunPodDeadlineSecs {
		t.Fatalf("pod spec should not outlive the session: %+v", pod.Spec)
	}
	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("containers: %d", len(pod.Spec.Containers))
	}
	c := pod.Spec.Containers[0]
	if !c.Stdin || !c.StdinOnce || !c.TTY {
		t.Fatalf("attach needs an interactive container: %+v", c)
	}
	if len(c.Env) != 2 || c.Env[0].Name != "SPARKWING_RERUN" || c.Env[1].Name != "SPARKWING_RUN_ID" {
		t.Fatalf("env should be sorted and complete: %+v", c.Env)
	}
	if c.Env[1].Value != "run-X" {
		t.Fatalf("env value lost: %+v", c.Env)
	}
}

func TestPrintRerunBanner_NamesDroppedKeys(t *testing.T) {
	snap := &store.NodeDispatch{
		RunID: "run-X", NodeID: "deploy",
		RedactedKeys: []byte(`["GITHUB_TOKEN","SPARKWING_AGENT_TOKEN"]`),
	}
	var buf bytes.Buffer
	printRerunBanner(&buf, snap, nil, "")

	out := buf.String()
	for _, want := range []string{"GITHUB_TOKEN", "SPARKWING_AGENT_TOKEN", "export"} {
		if !strings.Contains(out, want) {
			t.Errorf("banner missing %q\n%s", want, out)
		}
	}
}

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
