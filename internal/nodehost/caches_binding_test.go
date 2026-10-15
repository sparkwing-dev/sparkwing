package nodehost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/artifacts"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type cacheRecordProbe struct {
	blob.RecordStore
	commits []blob.CommitRecordRequest
}

func (*cacheRecordProbe) LookupDepcache(context.Context, blob.DepcacheLookup) (blob.DepcacheHit, error) {
	return blob.DepcacheHit{}, nil
}

func (p *cacheRecordProbe) CommitRecord(_ context.Context, q blob.CommitRecordRequest) (blob.Record, error) {
	p.commits = append(p.commits, q)
	return blob.Record{Key: q.Key, Digest: q.Digest, Source: q.Source}, nil
}

func TestCachesUseCurrentBindingAfterRetries(t *testing.T) {
	err := quick.Check(func(ordinal, generation uint8, refuseRestore, refuseSave bool) bool {
		f := newCacheFixture(t)
		f.run(modules(), "ambient", sparkwing.Success)
		if err := os.RemoveAll(filepath.Join(f.repo, "vendor")); err != nil {
			t.Fatal(err)
		}
		bytes, records := &memoTestBytes{}, &cacheRecordProbe{}
		calls := 0
		cfg := &Config{Session: SessionConfig{Dir: f.repo}, Artifacts: func(_ context.Context, n *NodeRun) (artifacts.Data, error) {
			calls++
			if calls == 1 && refuseRestore || calls > 1 && refuseSave {
				return artifacts.Data{}, errors.New("refused binding")
			}
			attempt := runspec.AttemptIdentity{Team: "team", RunID: "run", NodeID: "build", Attempt: int64(n.Attempt), Generation: int64(generation) + 1}
			identity := blob.UploadIdentity{Kind: blob.UploadJob, Team: attempt.Team, RunID: attempt.RunID, Principal: "principal", CapabilityRef: "cap", Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: attempt}}
			authority := blob.Authority{Principal: identity.Principal, CapabilityRef: identity.CapabilityRef, Attempt: identity.Attempt, Repo: "github.com/owner/repo", Ref: blob.RefFacts{Scope: blob.RefScope{Ref: "main"}}}
			return artifacts.Data{Bytes: bytes, Records: records, Authority: authority, Identity: identity, Budget: blob.Budget{Deadline: time.Now().Add(time.Minute)}}, nil
		}}
		n := &NodeRun{Plan: nodeproto.JobSpec{ID: "build", Caches: modules()}, Attempt: int(ordinal) + 1}
		cold := false
		res := cachesLayer(cfg, func(_ context.Context, n *NodeRun) Result {
			_, err := os.Stat(filepath.Join(f.repo, "vendor", "dep.txt"))
			cold = errors.Is(err, os.ErrNotExist)
			n.Attempt++
			return writeFile(f.repo, "vendor/dep.txt", "new install")
		})(t.Context(), n)
		if res.Outcome != sparkwing.Success || !cold {
			return false
		}
		if refuseRestore {
			return calls == 1 && len(bytes.puts) == 0 && len(records.commits) == 0
		}
		if refuseSave {
			return calls == 2 && len(bytes.puts) == 0 && len(records.commits) == 0
		}
		if calls != 2 || len(bytes.puts) != 1 || len(records.commits) != 1 {
			return false
		}
		identity := bytes.puts[0].Identity
		return identity.Attempt.Value.Attempt == int64(n.Attempt) && identity.Attempt.Value.Generation == int64(generation)+1 && records.commits[0].Source == identity
	}, &quick.Config{MaxCount: 64})
	if err != nil {
		t.Fatal(err)
	}
}
