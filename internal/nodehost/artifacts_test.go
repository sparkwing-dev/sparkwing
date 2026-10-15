package nodehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/artifacts"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	fsstore "github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type artifactHarness struct {
	t   *testing.T
	st  *store.Store
	art storage.ArtifactStore
	run *Run
}

func newArtifactHarness(t *testing.T, nodes ...string) *artifactHarness {
	t.Helper()
	st := openStore(t)
	art, err := fsstore.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := st.CreateRun(ctx, store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range nodes {
		if err := st.CreateNode(ctx, store.Node{RunID: testRunID, NodeID: id, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
	}
	run := &Run{ID: testRunID, State: st, Logger: slog.New(slog.DiscardHandler), validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}
	return &artifactHarness{t: t, st: st, art: art, run: run}
}

func (h *artifactHarness) exec(workspace string, plan nodeproto.JobSpec, body func(dir string) Result) (res Result, ran bool) {
	cfg := &Config{Artifacts: h.art, Session: SessionConfig{Dir: workspace, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}}}
	inner := func(context.Context, *NodeRun) Result {
		ran = true
		return body(workspace)
	}
	res = artifactsLayer(cfg, inner)(h.t.Context(), &NodeRun{Run: h.run, Plan: plan})
	return res, ran
}

func (h *artifactHarness) manifestOf(node string) string {
	h.t.Helper()
	row, err := h.st.GetNode(h.t.Context(), testRunID, node)
	if err != nil {
		h.t.Fatal(err)
	}
	return row.ArtifactManifest
}

func (h *artifactHarness) get(key string) []byte {
	h.t.Helper()
	rc, err := h.art.Get(h.t.Context(), key)
	if err != nil {
		h.t.Fatalf("get %s: %v", key, err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		h.t.Fatal(err)
	}
	return b
}

func writeFile(dir, rel, body string) Result {
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return Result{Outcome: sparkwing.Failed, Err: err}
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		return Result{Outcome: sparkwing.Failed, Err: err}
	}
	return Result{Outcome: sparkwing.Success}
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestArtifactsStageThenPublish(t *testing.T) {
	h := newArtifactHarness(t, "build", "package")
	build := nodeproto.JobSpec{ID: "build", Artifacts: []string{"dist/**"}, Steps: []nodeproto.StepSpec{{ID: "body"}}}
	res, _ := h.exec(t.TempDir(), build, func(dir string) Result { return writeFile(dir, "dist/app.bin", "binary") })
	if res.Outcome != sparkwing.Success {
		t.Fatalf("build: %s %v", res.Outcome, res.Err)
	}

	pkg := nodeproto.JobSpec{
		ID: "package", Needs: []string{"build"}, Artifacts: []string{"out.tgz"},
		Consumes: []nodeproto.ArtifactInput{{Producer: "build", Into: "in"}}, Steps: []nodeproto.StepSpec{{ID: "body"}},
	}
	res, _ = h.exec(t.TempDir(), pkg, func(dir string) Result {
		b, err := os.ReadFile(filepath.Join(dir, "in", "dist", "app.bin"))
		if err != nil || string(b) != "binary" {
			return Result{Outcome: sparkwing.Failed, Err: errors.New("consumed artifact not staged before the node ran")}
		}
		return writeFile(dir, "out.tgz", "packaged")
	})
	if res.Outcome != sparkwing.Success {
		t.Fatalf("package: %s %v", res.Outcome, res.Err)
	}

	digest := h.manifestOf("package")
	raw := h.get(artifacts.ManifestKey(digest))
	if sha(raw) != digest {
		t.Fatalf("row digest %s does not hash the stored manifest", digest)
	}
	var m struct {
		Entries []struct{ Path, Digest string } `json:"entries"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 1 || m.Entries[0].Path != "out.tgz" || m.Entries[0].Digest != sha([]byte("packaged")) {
		t.Fatalf("manifest = %+v", m.Entries)
	}
	if got := h.get(artifacts.BlobKey(m.Entries[0].Digest)); string(got) != "packaged" {
		t.Fatalf("blob = %q", got)
	}
}

func TestArtifactsFailedNodePublishesNothing(t *testing.T) {
	h := newArtifactHarness(t, "build")
	build := nodeproto.JobSpec{ID: "build", Artifacts: []string{"*"}, Steps: []nodeproto.StepSpec{{ID: "body"}}}
	res, _ := h.exec(t.TempDir(), build, func(dir string) Result {
		writeFile(dir, "half.bin", "partial")
		return Result{Outcome: sparkwing.Failed, Err: errors.New("boom")}
	})
	if res.Outcome != sparkwing.Failed || res.Err.Error() != "boom" {
		t.Fatalf("result = %s %v, want the inner failure unchanged", res.Outcome, res.Err)
	}
	if d := h.manifestOf("build"); d != "" {
		t.Fatalf("failed node recorded manifest %s", d)
	}
}

func TestArtifactsMissingConsumedArtifactFailsNode(t *testing.T) {
	h := newArtifactHarness(t, "build", "deploy")
	build := nodeproto.JobSpec{ID: "build", Artifacts: []string{"app.bin"}, Steps: []nodeproto.StepSpec{{ID: "body"}}}
	if res, _ := h.exec(t.TempDir(), build, func(dir string) Result { return writeFile(dir, "app.bin", "binary") }); res.Outcome != sparkwing.Success {
		t.Fatalf("build: %v", res.Err)
	}
	if err := h.art.Delete(t.Context(), artifacts.BlobKey(sha([]byte("binary")))); err != nil {
		t.Fatal(err)
	}

	deploy := nodeproto.JobSpec{ID: "deploy", Consumes: []nodeproto.ArtifactInput{{Producer: "build"}}, Steps: []nodeproto.StepSpec{{ID: "body"}}}
	res, ran := h.exec(t.TempDir(), deploy, func(string) Result { return Result{Outcome: sparkwing.Success} })
	if ran {
		t.Fatal("node ran without its consumed artifact")
	}
	if res.Outcome != sparkwing.Failed || res.Reason != store.FailureUnknown {
		t.Fatalf("result = %s reason %q", res.Outcome, res.Reason)
	}
	msg := res.Err.Error()
	for _, want := range []string{"stage consumed artifacts", `producer "build"`, `artifact "app.bin"`} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not say %q", msg, want)
		}
	}
}

func TestArtifactsRefuseEscapingDeclaredPaths(t *testing.T) {
	cases := map[string]nodeproto.JobSpec{
		"output climbs":   {ID: "n", Artifacts: []string{"../secret.txt"}},
		"output buried":   {ID: "n", Artifacts: []string{"dist/../../secret.txt"}},
		"output absolute": {ID: "n", Artifacts: []string{"/etc/passwd"}},
		"into climbs":     {ID: "n", Consumes: []nodeproto.ArtifactInput{{Producer: "build", Into: "../up"}}},
		"into absolute":   {ID: "n", Consumes: []nodeproto.ArtifactInput{{Producer: "build", Into: "/tmp/x"}}},
	}
	for name, plan := range cases {
		t.Run(name, func(t *testing.T) {
			h := newArtifactHarness(t, "build", "n")
			res, ran := h.exec(t.TempDir(), plan, func(string) Result { return Result{Outcome: sparkwing.Success} })
			if ran {
				t.Fatal("node ran with an escaping declared path")
			}
			if res.Outcome != sparkwing.Failed || !strings.Contains(res.Err.Error(), "inside the workspace") {
				t.Fatalf("result = %s %v", res.Outcome, res.Err)
			}
		})
	}
}

func TestArtifactsLayerOffWithoutStore(t *testing.T) {
	next := func(context.Context, *NodeRun) Result { return Result{Outcome: sparkwing.Success} }
	cfg := &Config{}
	res := artifactsLayer(cfg, next)(t.Context(), &NodeRun{Plan: nodeproto.JobSpec{ID: "n", Artifacts: []string{"../x"}, Steps: []nodeproto.StepSpec{{ID: "body"}}}})
	if res.Outcome != sparkwing.Success {
		t.Fatalf("layer acted with no store: %s %v", res.Outcome, res.Err)
	}
}
