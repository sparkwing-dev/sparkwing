package nodehost

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/artifacts"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: node "build" declares outputs, so a copy checks its artifacts; a miss runs a body that records it ran.
func memoArtifactRun(h *artifactHarness, src func() (MemoSource, error), ran *bool) *memoRun {
	m := &Memo{Slots: storeSlots{st: h.st}, Source: func(context.Context, store.ClaimInputRequest, string, string) (MemoSource, error) {
		return src()
	}}
	return &memoRun{
		cfg: &Config{Artifacts: h.art}, m: m, adm: &Admission{Slots: m.Slots},
		n: &NodeRun{Run: h.run, Plan: nodeproto.PlanNode{ID: "build", Outputs: []string{"app.bin"}}},
		next: func(context.Context, *NodeRun) Result {
			*ran = true
			return Result{Outcome: sparkwing.Success, Output: []byte(`"own"`)}
		},
		g: group{key: memoKeyPrefix + "h", policy: store.OnLimitCoalesce, capacity: 1, cost: 1, memoHash: "h"},
	}
}

func publishArtifact(t *testing.T, h *artifactHarness, node, body string) string {
	t.Helper()
	res, _ := h.exec(t.TempDir(), nodeproto.PlanNode{ID: node, Outputs: []string{"app.bin"}}, func(dir string) Result {
		return writeFile(dir, "app.bin", body)
	})
	if res.Outcome != sparkwing.Success {
		t.Fatal(res.Err)
	}
	return h.manifestOf(node)
}

func stagedArtifact(t *testing.T, h *artifactHarness) (Result, string) {
	t.Helper()
	dir := t.TempDir()
	res, _ := h.exec(dir, nodeproto.PlanNode{ID: "consumer", Consumes: []nodeproto.PlanConsume{{Producer: "build"}}},
		func(string) Result { return Result{Outcome: sparkwing.Success} })
	data, _ := os.ReadFile(filepath.Join(dir, "app.bin"))
	return res, string(data)
}

func copyFrom(r *memoRun, follow bool) (Result, bool) {
	if follow {
		return r.follow(context.Background(), "run-src", "source")
	}
	return r.hit(context.Background(), "run-src", "source", false)
}

// A hit or follower takes its source's output and artifacts from one read, so
// its consumer stages the files of the run whose output it copied.
func TestMemoCopyTakesOutputAndArtifactsFromOneSource(t *testing.T) {
	for name, follow := range map[string]bool{"Hit": false, "Follow": true} {
		t.Run(name, func(t *testing.T) {
			h := newArtifactHarness(t, "source", "build", "consumer")
			manifest := publishArtifact(t, h, "source", "artifact-A")
			ran := false
			res, copied := copyFrom(memoArtifactRun(h, func() (MemoSource, error) {
				return MemoSource{Output: []byte(`"source"`), ArtifactManifest: manifest}, nil
			}, &ran), follow)
			consumer, data := stagedArtifact(t, h)
			if ran || !copied || string(res.Output) != `"source"` || consumer.Outcome != sparkwing.Success || data != "artifact-A" {
				t.Fatalf("copy = %+v (ran %v, copied %v), consumer %s staged %q; want the source's output and artifact",
					res, ran, copied, consumer.Outcome, data)
			}
		})
	}
}

// A hit or follower whose source has no manifest, or whose manifest or blob
// is gone from the store, runs the node rather than hand its consumers
// artifacts they cannot stage.
func TestMemoCopyWithMissingArtifactsRunsAsAMiss(t *testing.T) {
	for _, gone := range []string{"none", "manifest", "blob"} {
		for name, follow := range map[string]bool{"Hit": false, "Follow": true} {
			t.Run(gone+"/"+name, func(t *testing.T) {
				h := newArtifactHarness(t, "source", "build", "consumer")
				manifest := publishArtifact(t, h, "source", "artifact-A")
				switch gone {
				case "none":
					manifest = ""
				case "manifest":
					if err := h.art.Delete(t.Context(), artifacts.ManifestKey(manifest)); err != nil {
						t.Fatal(err)
					}
				case "blob":
					if err := h.art.Delete(t.Context(), artifacts.BlobKey(sha([]byte("artifact-A")))); err != nil {
						t.Fatal(err)
					}
				}
				ran := false
				res, copied := copyFrom(memoArtifactRun(h, func() (MemoSource, error) {
					return MemoSource{Output: []byte(`"source"`), ArtifactManifest: manifest}, nil
				}, &ran), follow)
				if !ran || copied || string(res.Output) != `"own"` {
					t.Fatalf("copy = %+v (ran %v, copied %v), want the node run as a miss", res, ran, copied)
				}
			})
		}
	}
}
