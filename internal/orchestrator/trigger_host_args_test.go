//go:build unix

package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func (h *hostedTrigger) hostIn(t *testing.T, dir string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if err := HostTriggerRun(ctx, TriggerHost{
		ControllerURL: h.url, Token: h.token, Trigger: h.trig, Entry: pipelineentry.Binary(h.bin), Dir: dir,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}); err != nil {
		t.Fatalf("host %s: %v", h.trig.ID, err)
	}
}

// safety: the same controller and store, so a memo entry the first run records is the second's to hit.
func (h *hostedTrigger) another(t *testing.T, id string, args map[string]string) *hostedTrigger {
	t.Helper()
	ctx, now := store.WithoutCreditMetering(t.Context()), time.Now()
	if err := h.st.CreateTriggerWithRun(ctx, store.Trigger{
		ID: id, Pipeline: "podnode", Args: args, Repo: "acme/widgets", GitBranch: "main", GitSHA: strings.Repeat("b", 40), CreatedAt: now,
	}, store.Run{ID: id, Pipeline: "podnode", Status: "pending", Args: args, CreatedAt: now, StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	next := *h
	trig, err := client.NewWithToken(h.url, nil, h.token).ClaimTriggerFor(t.Context(), nil, nil)
	if err != nil || trig == nil || trig.ID != id {
		t.Fatalf("claim trigger %s: %+v %v", id, trig, err)
	}
	next.trig = trig
	return &next
}

func checkoutWith(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".sparkwing"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".sparkwing", "sparkwing.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readTrimmed(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// A job's typed args are bound from the run's arguments in the node's own
// process, so a dispatched node sees the value the run was started with.
func TestTriggerHost_BindsAJobsArgsFromTheRun(t *testing.T) {
	out := filepath.Join(t.TempDir(), "scale")
	h := newHostedTrigger(t, map[string]string{"envout": filepath.Join(t.TempDir(), "env"), "scaleout": out, "replicas": "5"})
	h.hostIn(t, t.TempDir())
	if got := readTrimmed(t, out); got != "replicas=5 zone=" {
		t.Fatalf("scale recorded %q, want the run's replicas=5", got)
	}
}

// The checkout's sparkwing.yaml supplies arguments a trigger left out, the
// pipeline's entry over the project defaults, and the trigger's own beat both.
func TestTriggerHost_LayersTheCheckoutsYAMLArgsUnderTheTriggers(t *testing.T) {
	yaml := "defaults:\n  args:\n    zone: eu-west\n    replicas: \"2\"\npipelines:\n  - name: podnode\n    entrypoint: Podnode\n    args:\n      replicas: \"3\"\n"
	out := filepath.Join(t.TempDir(), "scale")
	h := newHostedTrigger(t, map[string]string{"envout": filepath.Join(t.TempDir(), "env"), "scaleout": out})
	h.hostIn(t, checkoutWith(t, yaml))
	if got := readTrimmed(t, out); got != "replicas=3 zone=eu-west" {
		t.Fatalf("scale recorded %q, want the entry's replicas and the default zone", got)
	}
	stored := h.another(t, "run-hosted-stored", map[string]string{"envout": filepath.Join(t.TempDir(), "env"), "scaleout": out, "zone": "us-east"})
	stored.hostIn(t, checkoutWith(t, yaml))
	if got := readTrimmed(t, out); got != "replicas=3 zone=us-east" {
		t.Fatalf("scale recorded %q, want the trigger's zone over the checkout's", got)
	}
}

// Two memoized producers that a later run takes from the cache still hand
// their artifacts to an aggregator dispatched to a host of its own, in a
// checkout where neither producer ran.
func TestTriggerHost_AnAggregatorStagesCachedProducersArtifacts(t *testing.T) {
	t.Setenv(ArtifactStoreEnvVar, "fs://"+t.TempDir())
	out := filepath.Join(t.TempDir(), "combined")
	args := map[string]string{"envout": filepath.Join(t.TempDir(), "env"), "shards": out}
	first := newHostedTrigger(t, args)
	first.hostIn(t, t.TempDir())
	if got := readTrimmed(t, out); got != "one+two" {
		t.Fatalf("first run combined %q", got)
	}
	if err := os.Remove(out); err != nil {
		t.Fatal(err)
	}
	second := first.another(t, "run-hosted-cached", args)
	second.hostIn(t, t.TempDir())
	for _, id := range []string{"shard-1", "shard-2"} {
		if n := second.node(t, id); n.Outcome != string(sparkwing.Cached) {
			t.Fatalf("second run %s = %s/%s (%s), want cached", id, n.Status, n.Outcome, n.Error)
		}
	}
	if n := second.node(t, "aggregate"); n.Outcome != string(sparkwing.Success) {
		t.Fatalf("second run aggregate = %s/%s (%s), want success", n.Status, n.Outcome, n.Error)
	}
	if got := readTrimmed(t, out); got != "one+two" {
		t.Fatalf("second run combined %q, want both cached shards staged", got)
	}
}
