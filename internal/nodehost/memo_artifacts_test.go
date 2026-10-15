package nodehost

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/artifacts"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type memoTestRecords struct {
	blob.RecordStore
	hit     blob.MemoHit
	copyErr error
	commits []blob.CommitRecordRequest
}

func (r *memoTestRecords) CopyMemo(context.Context, blob.CopyMemoRequest) (blob.MemoHit, error) {
	return r.hit, r.copyErr
}
func (r *memoTestRecords) CommitRecord(_ context.Context, q blob.CommitRecordRequest) (blob.Record, error) {
	r.commits = append(r.commits, q)
	return blob.Record{Key: q.Key, Digest: q.Digest}, nil
}

type memoTestBytes struct {
	blob.Store
	body string
	err  error
	puts []blob.PutRequest
}

func (b *memoTestBytes) Get(context.Context, blob.GetRequest) (blob.VerifiedReader, error) {
	if b.err != nil {
		return nil, b.err
	}
	return &memoTestReader{Reader: strings.NewReader(b.body)}, nil
}
func (b *memoTestBytes) Put(_ context.Context, q blob.PutRequest) (blob.Receipt, error) {
	b.puts = append(b.puts, q)
	return blob.Receipt{Digest: q.Digest, Identity: q.Identity}, nil
}

type memoTestReader struct {
	*strings.Reader
	eof bool
}

func (r *memoTestReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		r.eof = true
	}
	return n, err
}
func (*memoTestReader) Close() error     { return nil }
func (r *memoTestReader) Verified() bool { return r.eof }

type memoTestState struct {
	storage.StateStoreOld
	starts   int
	startErr error
}

func (s *memoTestState) StartNode(context.Context, string, string) error {
	s.starts++
	return s.startErr
}

type memoTestSlots struct{ ConcurrencySlots }

func (memoTestSlots) AcquireSlot(_ context.Context, q store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	if q.BypassRead || q.CacheKeyHash != "" {
		return store.AcquireSlotResponse{}, errors.New("memo miss must coalesce without old cache lookup")
	}
	return store.AcquireSlotResponse{Kind: store.AcquireGranted}, nil
}
func (memoTestSlots) ReleaseSlot(context.Context, string, string, string, string, string, time.Duration) error {
	return nil
}

func TestMemoMaterializationRunsOnRefusal(t *testing.T) {
	for _, mode := range []string{"lookup", "materialize", "promoted"} {
		for _, failure := range []string{"none", "follower_output", "manifest_commit", "start"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				attempt := runspec.AttemptIdentity{Team: "team", RunID: "run", NodeID: "job", Attempt: 1, Generation: 1}
				identity := blob.UploadIdentity{Kind: blob.UploadJob, Team: attempt.Team, RunID: attempt.RunID, Principal: "principal", CapabilityRef: "cap", Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: attempt}}
				authority := blob.Authority{Principal: identity.Principal, CapabilityRef: identity.CapabilityRef, Attempt: identity.Attempt, Repo: "github.com/owner/repo", Ref: blob.RefFacts{Scope: blob.RefScope{Ref: "main"}}}
				bytes := &memoTestBytes{body: `"leader"`}
				records := &memoTestRecords{hit: blob.MemoHit{Found: true, Output: runspec.Optional[blob.Digest]{Set: true, Value: blob.Digest(fmt.Sprintf("%x", sha256.Sum256([]byte(bytes.body))))}}}
				state := &memoTestState{}
				switch failure {
				case "follower_output":
					bytes.err = blob.ErrNotFound
				case "manifest_commit":
					records.copyErr = errors.New("refused manifest commit")
				case "start":
					state.startErr = errors.New("refused start")
				}
				cfg := &Config{Artifacts: func(context.Context, *NodeRun) (artifacts.Data, error) {
					return artifacts.Data{Bytes: bytes, Records: records, Authority: authority, Identity: identity, Pipeline: "pipeline", Budget: blob.Budget{Deadline: time.Now().Add(time.Minute)}}, nil
				}}
				m := &Memo{Slots: memoTestSlots{}}
				ran := false
				r := &memoRun{cfg: cfg, m: m, adm: &Admission{Slots: m.Slots}, n: &NodeRun{Run: &Run{ID: "run", State: state, Logger: slog.New(slog.DiscardHandler)}, Plan: nodeproto.PlanNode{ID: "job"}}, g: group{key: "memo", policy: store.OnLimitCoalesce, capacity: 1, cost: 1, memoHash: strings.Repeat("a", 64)}, next: func(context.Context, *NodeRun) Result {
					ran = true
					return Result{Outcome: sparkwing.Success, Output: []byte(`"own"`), recorded: true}
				}}
				var got Result
				var copied bool
				if mode == "lookup" {
					got, copied = r.run(t.Context(), false)
				} else if mode == "promoted" {
					got, copied = r.hold(t.Context(), "holder")
				} else {
					got, copied = r.materialize(t.Context())
				}
				switch failure {
				case "none":
					if ran || !copied || string(got.Output) != `"leader"` || got.Outcome != sparkwing.Cached {
						t.Fatalf("result %+v copied=%v ran=%v", got, copied, ran)
					}
				case "start":
					if copied || got.Outcome != sparkwing.Failed {
						t.Fatalf("refused start became %+v", got)
					}
				default:
					if !ran || copied || string(got.Output) != `"own"` {
						t.Fatalf("result %+v copied=%v ran=%v", got, copied, ran)
					}
				}
				if failure == "none" {
					if err := r.save(t.Context(), []byte(`"own"`)); err != nil {
						t.Fatal(err)
					}
					if len(bytes.puts) != 1 || len(records.commits) != 1 {
						t.Fatal("missing verified output or memo record")
					}
					q := records.commits[0]
					if q.Key.Kind != blob.KindMemo || q.Key.Scope.Pipeline != "pipeline" || q.Key.Scope.Job != "job" || q.Key.Scope.Ref.Verified || q.Source != identity {
						t.Fatalf("memo scope %+v", q)
					}
				}
			})
		}
	}
}
