package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type outputRecords struct {
	RecordStore
	request     CommitRecordRequest
	result      RecordLookupResult
	err         error
	afterCommit func()
}

func (r *outputRecords) CommitRecord(_ context.Context, q CommitRecordRequest) (Record, error) {
	r.request = q
	if r.err != nil {
		return Record{}, r.err
	}
	r.result = RecordLookupResult{Found: true, Record: Record{Key: q.Key, Digest: q.Digest, Source: q.Source}}
	if r.afterCommit != nil {
		r.afterCommit()
	}
	return r.result.Record, nil
}

func TestOutputCommitPreservesCancellationCause(t *testing.T) {
	data, records, _ := newOutputData(t)
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	cause := errors.New("output writer stopped")
	records.afterCommit = func() { cancel(cause) }
	record, err := data.Put(ctx, []byte("result"))
	if !errors.Is(err, cause) || record != (Record{}) {
		t.Fatalf("cancelled commit returned %+v: %v", record, err)
	}
}

func newOutputData(t *testing.T) (OutputData, *outputRecords, *signer) {
	t.Helper()
	i := identity()
	objects := make(map[Digest]string)
	s := &signer{download: func(q GetRequest) (SignedTransfer, error) {
		body, ok := objects[q.Digest]
		if !ok {
			return SignedTransfer{}, ErrNotFound
		}
		return SignedTransfer{Digest: q.Digest, Size: int64(len(body))}, nil
	}}
	store := NewSignedStore(SignedStoreOptions{Signer: s, HTTPClient: transfer(func(_ context.Context, q SignedTransfer, body io.Reader) (io.ReadCloser, error) {
		if body != nil {
			raw, err := io.ReadAll(body)
			if err != nil {
				return nil, err
			}
			objects[q.Digest] = string(raw)
			return io.NopCloser(strings.NewReader("")), nil
		}
		return io.NopCloser(strings.NewReader(objects[q.Digest])), nil
	})})
	r := &outputRecords{}
	return OutputData{
		Bytes: store, Records: r, Identity: i, Pipeline: "build",
		Authority: Authority{CapabilityRef: i.CapabilityRef, Principal: i.Principal, Attempt: i.Attempt, Repo: "gh:123", Ref: RefFacts{Scope: RefScope{Ref: "feature"}}},
	}, r, s
}

func TestOutputRecordRoundTrip(t *testing.T) {
	for _, body := range []string{"", "null", `{"answer":42}`, strings.Repeat("x", int(limits.NodeOutputBytes))} {
		t.Run(string(digest(body))[:8], func(t *testing.T) {
			data, records, signer := newOutputData(t)
			record, err := data.Put(t.Context(), []byte(body))
			if err != nil {
				t.Fatal(err)
			}
			key := record.Key
			if key != ResultRecordKey(data.Identity.Attempt.Value, data.Authority.Repo, data.Authority.Ref.Scope, data.Pipeline, KindOutput) {
				t.Fatalf("output key = %+v", key)
			}
			if record.Digest != digest(body) || signer.commits != 1 || records.request.Source != data.Identity || records.request.Authority != data.Authority || records.request.ManifestDigest.Set || len(records.request.Members) != 0 {
				t.Fatalf("output commit = %+v", records.request)
			}
		})
	}
}

func TestOutputWriteRefusals(t *testing.T) {
	for _, fault := range []string{"output_limit", "wrong_attempt", "wrong_principal", "wrong_generation", "owner", "commit", "cancelled"} {
		t.Run(fault, func(t *testing.T) {
			data, records, signer := newOutputData(t)
			body := "result"
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch fault {
			case "output_limit":
				body = strings.Repeat("x", int(limits.NodeOutputBytes)+1)
			case "wrong_attempt":
				data.Identity.Attempt.Value.Attempt++
			case "wrong_principal":
				data.Identity.Principal = "other"
			case "wrong_generation":
				data.Identity.Attempt.Value.Generation++
			case "owner":
				data.Identity.Kind = UploadOwnerUnit
				data.Identity.Attempt = runspec.Optional[runspec.AttemptIdentity]{}
				data.Identity.Unit = runspec.Optional[UploadUnit]{Set: true, Value: UploadUnit{Kind: UploadPlanUnit, ID: "plan", Generation: 3}}
				data.Authority.Attempt = runspec.Optional[runspec.AttemptIdentity]{}
				data.Authority.Upload = runspec.Optional[UploadIdentity]{Set: true, Value: data.Identity}
			case "commit":
				records.err = errors.New("record commit refused")
			case "cancelled":
				cancel()
			}
			if record, err := data.Put(ctx, []byte(body)); err == nil || record.Digest != "" {
				t.Fatalf("invalid output succeeded: %+v, %v", record, err)
			}
			if fault != "commit" && (signer.commits != 0 || records.request.Digest != "") {
				t.Fatal("invalid output reached byte or record commit")
			}
		})
	}
}

func TestOutputImportedPublication(t *testing.T) {
	data, records, signer := newOutputData(t)
	data.Identity.Kind = UploadPublication
	data.Authority.Upload = runspec.Optional[UploadIdentity]{Set: true, Value: data.Identity}
	record, err := data.Put(t.Context(), []byte(`{"published":true}`))
	if err != nil || record.Source != data.Identity || records.request.Source.Kind != UploadPublication || signer.intent.Identity != data.Identity {
		t.Fatalf("imported output = %+v, %v", record, err)
	}
}

type cancelledOutputUpload struct {
	Store
	cancel context.CancelFunc
}

func (s cancelledOutputUpload) Put(ctx context.Context, q PutRequest) (Receipt, error) {
	receipt, err := s.Store.Put(ctx, q)
	s.cancel()
	return receipt, err
}

func TestOutputCancellationAfterUploadPreventsRecordCommit(t *testing.T) {
	data, records, signer := newOutputData(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	data.Bytes = cancelledOutputUpload{Store: data.Bytes, cancel: cancel}
	if record, err := data.Put(ctx, []byte("result")); record.Digest != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled output = %+v, %v", record, err)
	}
	if signer.commits != 1 || records.request.Digest != "" {
		t.Fatal("record commit followed a cancelled upload")
	}
}

func TestResultRecordKeysKeepAttemptFence(t *testing.T) {
	property := func(seed [16]byte, attempt, generation uint16) bool {
		i := identity()
		i.RunID = fmt.Sprintf("run-%x", seed[:])
		i.Attempt.Value.RunID = i.RunID
		i.Attempt.Value.Attempt = int64(attempt) + 1
		i.Attempt.Value.Generation = int64(generation) + 1
		a := Authority{CapabilityRef: i.CapabilityRef, Principal: i.Principal, Attempt: i.Attempt, Repo: "gh:1", Ref: RefFacts{Scope: RefScope{Ref: "main"}}}
		for _, kind := range []Kind{KindOutput, KindManifest} {
			intent := WriteScopeIntent{Kind: kind, Pipeline: "ci", Job: i.Attempt.Value.NodeID, Name: i.RunID}
			key, err := ResolveWriteKey(a, intent)
			if err != nil || key != ResultRecordKey(i.Attempt.Value, a.Repo, a.Ref.Scope, "ci", kind) {
				return false
			}
			namespace, err := RecordNamespace(key)
			if err != nil {
				return false
			}
			for _, change := range []func(*runspec.AttemptIdentity){
				func(a *runspec.AttemptIdentity) { a.Team += "other" },
				func(a *runspec.AttemptIdentity) { a.RunID += "other" },
				func(a *runspec.AttemptIdentity) { a.NodeID += "other" },
				func(a *runspec.AttemptIdentity) { a.Attempt++ },
				func(a *runspec.AttemptIdentity) { a.Generation++ },
			} {
				other := i.Attempt.Value
				change(&other)
				otherKey := ResultRecordKey(other, a.Repo, a.Ref.Scope, "ci", kind)
				otherNamespace, err := RecordNamespace(otherKey)
				if err != nil || (key.Team == otherKey.Team && namespace == otherNamespace) {
					return false
				}
			}
			for _, change := range []func(*WriteScopeIntent){
				func(q *WriteScopeIntent) { q.Job += "other" },
				func(q *WriteScopeIntent) { q.Name += "other" },
				func(q *WriteScopeIntent) { q.Key = "other" },
			} {
				forged := intent
				change(&forged)
				if _, err := ResolveWriteKey(a, forged); !errors.Is(err, ErrDigestNotReadable) {
					return false
				}
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 96, Rand: rand.New(rand.NewSource(2097))}); err != nil {
		t.Fatal(err)
	}
}
