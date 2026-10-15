package nodehost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/artifacts"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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

func TestArtifactsRequireAuthenticatedBinding(t *testing.T) {
	for _, declared := range []nodeproto.JobSpec{
		{ID: "build", Artifacts: []string{"*.bin"}},
		{ID: "deploy", Consumes: []nodeproto.ArtifactInput{{Producer: "build"}}},
	} {
		t.Run(declared.ID, func(t *testing.T) {
			ran := false
			cfg := &Config{Session: SessionConfig{Dir: t.TempDir()}}
			got := artifactsLayer(cfg, func(context.Context, *NodeRun) Result { ran = true; return Result{Outcome: sparkwing.Success} })(t.Context(), &NodeRun{Plan: declared})
			if ran || got.Outcome != sparkwing.Failed || !strings.Contains(got.Err.Error(), "authenticated storage binding") {
				t.Fatalf("result %+v ran=%v", got, ran)
			}
		})
	}
}

func TestArtifactsBindingFailurePrecedesExecution(t *testing.T) {
	want := errors.New("binding revoked")
	cfg := &Config{Session: SessionConfig{Dir: t.TempDir()}, Artifacts: func(context.Context, *NodeRun) (artifacts.Data, error) { return artifacts.Data{}, want }}
	got := artifactsLayer(cfg, func(context.Context, *NodeRun) Result { t.Fatal("ran after refused binding"); return Result{} })(t.Context(), &NodeRun{Plan: nodeproto.JobSpec{ID: "job", Artifacts: []string{"*"}}})
	if !errors.Is(got.Err, want) {
		t.Fatal(got.Err)
	}
}

func TestArtifactsStageRefusalPrecedesExecution(t *testing.T) {
	records := &refusedArtifactRecords{}
	cfg := &Config{Session: SessionConfig{Dir: t.TempDir()}, Artifacts: func(context.Context, *NodeRun) (artifacts.Data, error) {
		return artifacts.Data{Bytes: unusedArtifactBytes{}, Records: records, Identity: blob.UploadIdentity{Team: "team"}, Pipeline: "pipeline"}, nil
	}}
	got := artifactsLayer(cfg, func(context.Context, *NodeRun) Result { t.Fatal("ran without required artifacts"); return Result{} })(t.Context(), &NodeRun{Run: &Run{ID: "run"}, Plan: nodeproto.JobSpec{ID: "job", Consumes: []nodeproto.ArtifactInput{{Producer: "build"}}}})
	if got.Outcome != sparkwing.Failed {
		t.Fatalf("result %+v", got)
	}
}

type unusedArtifactBytes struct{ blob.Store }
type refusedArtifactRecords struct{ blob.RecordStore }

func (*refusedArtifactRecords) LookupRecord(context.Context, blob.RecordLookup) (blob.RecordLookupResult, error) {
	return blob.RecordLookupResult{}, blob.ErrDigestNotReadable
}
