package blob_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type records struct {
	blob.RecordStore
	lookup func(context.Context, blob.RecordLookup) (blob.RecordLookupResult, error)
}

func (r records) LookupRecord(ctx context.Context, q blob.RecordLookup) (blob.RecordLookupResult, error) {
	return r.lookup(ctx, q)
}

func TestRemoteCacheVerifiedDefaultAndArtifactEdges(t *testing.T) {
	d := digest("bytes")
	member := digest("artifact")
	manifestBody := fmt.Sprintf(`{"entries":[{"path":"out","digest":"%s","size":8,"mode":420}]}`, member)
	manifest := digest(manifestBody)
	key := blob.MemoKey{Repo: "gh:123", Ref: blob.RefScope{Ref: "feature"}, Pipeline: "build", Job: "job", Hash: runspec.Hash(digest("key"))}
	result := blob.RecordLookupResult{Found: true, Record: blob.Record{
		Key:    blob.RecordKey{Team: "team", Kind: blob.KindMemo, Name: blob.MemoRecordName, Scope: blob.Scope{Repo: key.Repo, Ref: blob.RefScope{Ref: "main", Verified: true}, Pipeline: key.Pipeline, Job: key.Job, Key: string(key.Hash)}},
		Digest: d, ManifestDigest: runspec.Optional[blob.Digest]{Set: true, Value: manifest},
		RefFacts: blob.RefFacts{Scope: blob.RefScope{Ref: "main", Verified: true}, DefaultBranch: "main", DefaultBranchVerified: true, AppDeliveryKey: "verified-delivery", SHA: "sealed-sha"},
	}, Members: []blob.Member{{Team: "team", ManifestDigest: manifest, Digest: member, Path: "out", Size: 8, Mode: 0o644}}}
	bodies := map[blob.Digest]string{d: "bytes", manifest: manifestBody, member: "artifact"}
	s := &signer{download: func(q blob.GetRequest) (blob.SignedTransfer, error) {
		body, ok := bodies[q.Digest]
		if !ok {
			return blob.SignedTransfer{}, blob.ErrNotFound
		}
		return blob.SignedTransfer{Digest: q.Digest, Size: int64(len(body))}, nil
	}}
	bytes := blob.NewSignedStore(blob.SignedStoreOptions{Signer: s, HTTPClient: transfer(func(_ context.Context, tr blob.SignedTransfer, _ io.Reader) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(bodies[tr.Digest])), nil
	})})
	r := records{lookup: func(_ context.Context, q blob.RecordLookup) (blob.RecordLookupResult, error) {
		if q.Key.Name != blob.MemoRecordName {
			t.Fatalf("memo lookup cannot find the committed result record: %+v", q.Key)
		}
		if !q.VerifiedDefaultOnly || q.Authority.CapabilityRef != "user-repo-read" || q.Authority.Repo != key.Repo || q.Key.Scope.Ref.Ref != "" {
			t.Fatal("lookup did not request a server-derived default branch")
		}
		return result, nil
	}}
	options := blob.RemoteCacheOptions{Profile: "ci", Team: "team", Authority: blob.Authority{CapabilityRef: "user-repo-read", Principal: "reader", Repo: key.Repo}, Repo: key.Repo, Records: r}
	cache, err := blob.OpenRemoteCache(options, bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cache.Artifact(t.Context(), d); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatal("arbitrary artifact was readable")
	}
	budget := blob.Budget{Deadline: time.Now().Add(time.Minute)}
	if hit, err := cache.Memo(t.Context(), key, blob.Budget{Deadline: time.Now().Add(-time.Second)}); err != nil || hit.Found {
		t.Fatalf("expired memo budget=%+v %v", hit, err)
	}
	hit, err := cache.Memo(t.Context(), key, budget)
	if err != nil || !hit.Found {
		t.Fatalf("memo=%+v, %v", hit, err)
	}
	reader, err := cache.Artifact(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(io.Discard, reader); err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	if !reader.Verified() {
		t.Fatal("artifact not verified")
	}
	for _, digest := range []blob.Digest{manifest, member} {
		reader, err := cache.Artifact(t.Context(), digest)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(io.Discard, reader)
		_ = reader.Close()
		if err != nil || !reader.Verified() {
			t.Fatalf("cached member=%v", err)
		}
	}
	if _, err = cache.Artifact(t.Context(), digest("foreign")); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatal("unrelated artifact was readable")
	}
	for _, mutation := range []string{"manual", "false_default", "foreign_team", "foreign_repo", "foreign_pipeline", "foreign_job", "foreign_hash", "foreign_name", "missing_delivery", "different_default", "foreign_member", "member_size", "member_path", "member_mode", "manifest_members", "corrupt"} {
		t.Run(mutation, func(t *testing.T) {
			bad := result
			bad.Members = append([]blob.Member(nil), result.Members...)
			switch mutation {
			case "manual":
				bad.Record.RefFacts.Scope.Verified = false
			case "false_default":
				bad.Record.RefFacts.DefaultBranchVerified = false
			case "foreign_team":
				bad.Record.Key.Team = "other"
			case "foreign_repo":
				bad.Record.Key.Scope.Repo = "gh:456"
			case "foreign_pipeline":
				bad.Record.Key.Scope.Pipeline = "other"
			case "foreign_job":
				bad.Record.Key.Scope.Job = "other"
			case "foreign_hash":
				bad.Record.Key.Scope.Key = string(digest("other"))
			case "foreign_name":
				bad.Record.Key.Name = "other"
			case "missing_delivery":
				bad.Record.RefFacts.AppDeliveryKey = ""
			case "different_default":
				bad.Record.RefFacts.DefaultBranch = "release"
			case "foreign_member":
				bad.Members[0].Team = "other"
			case "member_size":
				bad.Members[0].Size++
			case "member_path":
				bad.Members[0].Path = "other"
			case "member_mode":
				bad.Members[0].Mode = 0o755
			case "manifest_members":
				bad.Members = nil
			case "corrupt":
				bad.Record.Digest = digest("other")
			}
			options.Records = records{lookup: func(context.Context, blob.RecordLookup) (blob.RecordLookupResult, error) { return bad, nil }}
			isolated, err := blob.OpenRemoteCache(options, bytes)
			if err != nil {
				t.Fatal(err)
			}
			hit, err := isolated.Memo(t.Context(), key, budget)
			if err != nil || hit.Found {
				t.Fatalf("unsafe memo hit: %+v, %v", hit, err)
			}
			if _, err := isolated.Artifact(t.Context(), d); !errors.Is(err, blob.ErrDigestNotReadable) {
				t.Fatal("rejected record granted artifact access")
			}
		})
	}
}

func TestRemoteCacheRequiresOptIn(t *testing.T) {
	if _, err := blob.OpenRemoteCache(blob.RemoteCacheOptions{}, nil); err == nil {
		t.Fatal("implicit remote cache")
	}
}

type remoteBody struct {
	io.Reader
	closed bool
	err    error
}

func (b *remoteBody) Close() error { b.closed = true; return b.err }

func TestRemoteMemoVerifiesBeforeGrantingArtifacts(t *testing.T) {
	for _, fault := range []string{"output_limit", "close", "lookup_cancelled", "read_cancelled"} {
		t.Run(fault, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			output := "result"
			if fault == "output_limit" {
				output = strings.Repeat("x", int(limits.NodeOutputBytes)+1)
			}
			d := digest(output)
			body := &remoteBody{Reader: strings.NewReader(output)}
			if fault == "close" {
				body.err = errors.New("close failed")
			}
			bytes := blob.NewSignedStore(blob.SignedStoreOptions{
				Signer: &signer{transfer: blob.SignedTransfer{Digest: d, Size: int64(len(output))}},
				HTTPClient: transfer(func(context.Context, blob.SignedTransfer, io.Reader) (io.ReadCloser, error) {
					if fault == "read_cancelled" {
						cancel()
					}
					return body, nil
				}),
			})
			records := records{lookup: func(_ context.Context, q blob.RecordLookup) (blob.RecordLookupResult, error) {
				if fault == "lookup_cancelled" {
					cancel()
					return blob.RecordLookupResult{}, ctx.Err()
				}
				key := q.Key
				key.Team, key.Scope.Ref = "team", blob.RefScope{Ref: "main", Verified: true}
				return blob.RecordLookupResult{Found: true, Record: blob.Record{Key: key, Digest: d, RefFacts: blob.RefFacts{
					Scope: key.Scope.Ref, DefaultBranch: "main", DefaultBranchVerified: true, AppDeliveryKey: "delivery", SHA: "sha",
				}}}, nil
			}}
			cache, err := blob.OpenRemoteCache(blob.RemoteCacheOptions{Profile: "ci", Team: "team", Authority: blob.Authority{CapabilityRef: "user", Principal: "reader", Repo: "gh:123"}, Repo: "gh:123", Records: records}, bytes)
			if err != nil {
				t.Fatal(err)
			}
			hit, err := cache.Memo(ctx, blob.MemoKey{Repo: "gh:123", Ref: blob.RefScope{Ref: "feature"}, Pipeline: "build", Job: "job", Hash: runspec.Hash(digest("key"))}, blob.Budget{Deadline: time.Now().Add(time.Minute)})
			if hit.Found || (ctx.Err() != nil && !errors.Is(err, context.Canceled)) || (ctx.Err() == nil && err != nil) {
				t.Fatalf("failed memo = %+v, %v", hit, err)
			}
			if fault != "lookup_cancelled" && !body.closed {
				t.Fatal("download was not closed")
			}
			if _, err := cache.Artifact(t.Context(), d); !errors.Is(err, blob.ErrDigestNotReadable) {
				t.Fatalf("failed memo granted artifacts: %v", err)
			}
		})
	}
}

func TestRemoteDepcacheArchiveIsReadableAfterTrustedLookup(t *testing.T) {
	d := digest("archive")
	s := &signer{transfer: blob.SignedTransfer{Digest: d, Size: 7}}
	bytes := blob.NewSignedStore(blob.SignedStoreOptions{Signer: s, HTTPClient: transfer(func(context.Context, blob.SignedTransfer, io.Reader) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("archive")), nil
	})})
	r := records{lookup: func(_ context.Context, q blob.RecordLookup) (blob.RecordLookupResult, error) {
		if !q.VerifiedDefaultOnly || q.Key.Kind != blob.KindDepcache {
			t.Fatal("unscoped depcache lookup")
		}
		key := q.Key
		key.Team = "team"
		key.Scope.Ref = blob.RefScope{Ref: "main", Verified: true}
		return blob.RecordLookupResult{Found: true, Record: blob.Record{Key: key, Digest: d, RefFacts: blob.RefFacts{Scope: key.Scope.Ref, DefaultBranch: "main", DefaultBranchVerified: true, AppDeliveryKey: "delivery", SHA: "sha"}}}, nil
	}}
	cache, err := blob.OpenRemoteCache(blob.RemoteCacheOptions{Profile: "ci", Team: "team", Authority: blob.Authority{CapabilityRef: "user", Principal: "reader", Repo: "gh:123"}, Repo: "gh:123", Records: r}, bytes)
	if err != nil {
		t.Fatal(err)
	}
	key := blob.DepcacheKey{Repo: "gh:123", Ref: blob.RefScope{Ref: "feature"}, Name: "modules", Key: "lock-hash"}
	record, err := cache.Depcache(t.Context(), key, blob.Budget{Deadline: time.Now().Add(time.Minute)})
	if err != nil || record.Digest != d {
		t.Fatalf("depcache=%+v %v", record, err)
	}
	reader, err := cache.Artifact(t.Context(), record.Digest)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, reader)
	_ = reader.Close()
	if err != nil || !reader.Verified() {
		t.Fatalf("archive=%v", err)
	}
}

func TestOwnerAndPublicationReceipts(t *testing.T) {
	owner := identity()
	owner.Kind = blob.UploadOwnerUnit
	owner.Attempt = runspec.Optional[runspec.AttemptIdentity]{}
	owner.Unit = runspec.Optional[blob.UploadUnit]{Set: true, Value: blob.UploadUnit{Kind: blob.UploadPlanUnit, ID: "plan", Generation: 3}}
	a := blob.Authority{Principal: owner.Principal, CapabilityRef: owner.CapabilityRef, Upload: runspec.Optional[blob.UploadIdentity]{Set: true, Value: owner}}
	for _, kind := range []blob.Kind{blob.KindBinary, blob.KindSourceBundle} {
		if err := blob.AuthorizeUpload(a, owner, kind); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []blob.Kind{blob.KindOutput, blob.KindManifest, blob.KindMemo, blob.KindDepcache} {
		if err := blob.AuthorizeUpload(a, owner, kind); !errors.Is(err, blob.ErrDigestNotReadable) {
			t.Fatalf("owner uploaded %s: %v", kind, err)
		}
	}
	bad := owner
	bad.Unit.Value.Generation++
	if err := blob.AuthorizeUpload(a, bad, blob.KindBinary); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatal("owner fence transferred")
	}
	job := identity()
	edge := blob.EdgeRequest{TargetKind: blob.KindOutput, Digest: digest("bytes"), Authority: blob.Authority{Principal: job.Principal, CapabilityRef: job.CapabilityRef, Attempt: job.Attempt}, OwnReceipt: runspec.Optional[blob.Receipt]{Set: true, Value: blob.Receipt{Identity: owner, Digest: digest("bytes")}}}
	if err := blob.AuthorizeEdge(edge); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatal("owner receipt became job authority")
	}
	published := job
	published.Kind = blob.UploadPublication
	a = blob.Authority{Principal: published.Principal, CapabilityRef: published.CapabilityRef, Attempt: published.Attempt, Upload: runspec.Optional[blob.UploadIdentity]{Set: true, Value: published}}
	if err := blob.AuthorizeUpload(a, published, blob.KindOutput); err != nil {
		t.Fatal(err)
	}
	if err := blob.AuthorizeUpload(a, published, blob.KindMemo); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatal("publication gained memo write")
	}
	bad = published
	bad.Attempt.Value.NodeID = "other"
	if err := blob.AuthorizeUpload(a, bad, blob.KindOutput); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatal("publication gained another job")
	}
}
