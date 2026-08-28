package orchestrator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestEnvelopeTruncated(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want bool
	}{
		{"empty", nil, false},
		{"normal", []byte(`{"version":1,"type_name":"Job","scalar_fields":{}}`), false},
		{"stub", []byte(`{"version":1,"truncated":true,"reason":"size","original_size":99}`), true},
		{"unrelated json", []byte(`{"truncated":false}`), false},
		{"malformed", []byte(`{not json`), false},
	}
	for _, tc := range cases {
		got := envelopeTruncated(tc.in)
		if got != tc.want {
			t.Errorf("%s: envelopeTruncated = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestMintReplayRun(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	if err := st.CreateRun(ctx, store.Run{
		ID:        "orig-1",
		Pipeline:  "deploy",
		Status:    "failed",
		StartedAt: time.Now(),
		GitSHA:    "abc123",
		Args:      map[string]string{"region": "us-east"},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: "orig-1", NodeID: "build", Status: "done",
		Deps: []string{"checkout"},
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := st.WriteNodeDispatch(ctx, store.NodeDispatch{
		RunID: "orig-1", NodeID: "build", Seq: 0,
		InputEnvelope: []byte(`{"version":1,"type_name":"Build","scalar_fields":{"target":"prod"}}`),
	}); err != nil {
		t.Fatalf("WriteNodeDispatch: %v", err)
	}

	newRunID, err := MintReplayRun(ctx, st, "orig-1", "build")
	if err != nil {
		t.Fatalf("MintReplayRun: %v", err)
	}
	if newRunID == "" || newRunID == "orig-1" {
		t.Fatalf("newRunID looks wrong: %q", newRunID)
	}

	got, err := st.GetRun(ctx, newRunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.ReplayOfRunID != "orig-1" || got.ReplayOfNodeID != "build" {
		t.Fatalf("replay lineage: got (%q,%q)", got.ReplayOfRunID, got.ReplayOfNodeID)
	}
	if got.Pipeline != "deploy" || got.GitSHA != "abc123" {
		t.Fatalf("pipeline/git inheritance broken: %+v", got)
	}
	if got.Args["region"] != "us-east" {
		t.Fatalf("args inheritance broken: %v", got.Args)
	}

	orig, err := st.GetRun(ctx, "orig-1")
	if err != nil {
		t.Fatalf("GetRun orig: %v", err)
	}
	if orig.ReplayOfRunID != "" {
		t.Fatalf("original run mutated: replay_of_run_id=%q", orig.ReplayOfRunID)
	}

	node, err := st.GetNode(ctx, newRunID, "build")
	if err != nil {
		t.Fatalf("GetNode replay: %v", err)
	}
	if node.Status != "pending" {
		t.Fatalf("replay node status: %q", node.Status)
	}
	if len(node.Deps) != 1 || node.Deps[0] != "checkout" {
		t.Fatalf("replay node deps: %v", node.Deps)
	}
}

func TestMintReplayRun_NoSnapshot(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	if err := st.CreateRun(ctx, store.Run{
		ID: "orig-1", Pipeline: "deploy", Status: "failed", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: "orig-1", NodeID: "build", Status: "done",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := MintReplayRun(ctx, st, "orig-1", "build"); err == nil {
		t.Fatalf("expected error when snapshot missing")
	}
}

func TestRunReplayNode_CodeDrift(t *testing.T) {

	pointRuntimeAt(t, t.TempDir())
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	paths := PathsAt(dir)

	if err := st.CreateRun(ctx, store.Run{
		ID: "orig-1", Pipeline: "no-such-pipeline",
		Status: "failed", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: "orig-1", NodeID: "build", Status: "done",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteNodeDispatch(ctx, store.NodeDispatch{
		RunID: "orig-1", NodeID: "build", Seq: 0,
		InputEnvelope: []byte(`{"version":1,"type_name":"old.Type","scalar_fields":{}}`),
	}); err != nil {
		t.Fatal(err)
	}
	newRunID, err := MintReplayRun(ctx, st, "orig-1", "build")
	if err != nil {
		t.Fatalf("MintReplayRun: %v", err)
	}

	if _, err := RunReplayNode(ctx, paths, st, newRunID, "build", nil); err == nil {
		t.Fatalf("expected failure when pipeline isn't registered")
	}
}

func TestRunReplayNode_NotAReplayRun(t *testing.T) {
	pointRuntimeAt(t, t.TempDir())
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	paths := PathsAt(dir)

	if err := st.CreateRun(ctx, store.Run{
		ID: "regular", Pipeline: "p", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := RunReplayNode(ctx, paths, st, "regular", "build", nil); err == nil {
		t.Fatalf("expected failure on non-replay run")
	}
}

func TestMintReplayRun_InheritsSecretArgClassification(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	const secret = "s3cr3t-token-value"
	if err := st.CreateRun(ctx, store.Run{
		ID: "orig-1", Pipeline: "deploy", Status: "failed",
		StartedAt: time.Now(),
		Args:      map[string]string{"token": secret, "env": "prod"},
		Invocation: map[string]any{
			"args":                        map[string]string{"token": secret, "env": "prod"},
			store.InvocationSecretArgsKey: []string{"token"},
		},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "orig-1", NodeID: "build", Status: "done"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := st.WriteNodeDispatch(ctx, store.NodeDispatch{
		RunID: "orig-1", NodeID: "build", Seq: 0,
		InputEnvelope: []byte(`{"version":1,"type_name":"Build"}`),
	}); err != nil {
		t.Fatalf("WriteNodeDispatch: %v", err)
	}

	newRunID, err := MintReplayRun(ctx, st, "orig-1", "build")
	if err != nil {
		t.Fatalf("MintReplayRun: %v", err)
	}
	replay, err := st.GetRun(ctx, newRunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got := replay.SecretArgNames(); len(got) != 1 || got[0] != "token" {
		t.Fatalf("replay classification = %v, want [token]", got)
	}
	if replay.RedactedForDisplay().Args["token"] != store.RedactedArgValue {
		t.Errorf("replay run renders the secret arg in the clear")
	}

	if replay.Args["token"] != secret {
		t.Errorf("replay run args[token] = %q, want plaintext", replay.Args["token"])
	}
}

func TestMintReplayRun_UnclassifiedSourceStaysUnclassified(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	if err := st.CreateRun(ctx, store.Run{
		ID: "orig-1", Pipeline: "deploy", Status: "failed",
		StartedAt: time.Now(), Args: map[string]string{"env": "prod"},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "orig-1", NodeID: "build", Status: "done"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := st.WriteNodeDispatch(ctx, store.NodeDispatch{
		RunID: "orig-1", NodeID: "build", Seq: 0,
		InputEnvelope: []byte(`{"version":1,"type_name":"Build"}`),
	}); err != nil {
		t.Fatalf("WriteNodeDispatch: %v", err)
	}
	newRunID, err := MintReplayRun(ctx, st, "orig-1", "build")
	if err != nil {
		t.Fatalf("MintReplayRun: %v", err)
	}
	replay, err := st.GetRun(ctx, newRunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if replay.Invocation != nil {
		t.Errorf("unclassified source produced an invocation: %v", replay.Invocation)
	}
}
