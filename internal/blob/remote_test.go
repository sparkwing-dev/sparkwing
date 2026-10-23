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
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type records struct {
	RecordStore
	lookup func(context.Context, RecordLookup) (RecordLookupResult, error)
}

func TestRemoteMemoMissingReferencedBytesFailProperty(t *testing.T) {
	property := func(body []byte, absent uint8) bool {
		output, member := string(body), "artifact:"+string(body)
		outputDigest, memberDigest := digest(output), digest(member)
		manifestBody := fmt.Sprintf(`{"entries":[{"path":"out","digest":"%s","size":%d,"mode":420}]}`, memberDigest, len(member))
		manifestDigest := digest(manifestBody)
		bodies := map[Digest]string{outputDigest: output, manifestDigest: manifestBody, memberDigest: member}
		missing := []Digest{outputDigest, manifestDigest, memberDigest}[absent%3]
		bytes := NewSignedStore(SignedStoreOptions{
			Signer: &signer{download: func(q GetRequest) (SignedTransfer, error) {
				return SignedTransfer{Digest: q.Digest, Size: int64(len(bodies[q.Digest]))}, nil
			}},
			HTTPClient: transfer(func(_ context.Context, tr SignedTransfer, _ io.Reader) (io.ReadCloser, error) {
				if tr.Digest == missing {
					return nil, ErrNotFound
				}
				return io.NopCloser(strings.NewReader(bodies[tr.Digest])), nil
			}),
		})
		r := records{lookup: func(_ context.Context, q RecordLookup) (RecordLookupResult, error) {
			key := q.Key
			key.Team, key.Scope.Ref = "team", RefScope{Ref: "main", Verified: true}
			return RecordLookupResult{Found: true, VerifiedDefault: true, Record: Record{Key: key, Digest: outputDigest, ManifestDigest: runspec.Optional[Digest]{Set: true, Value: manifestDigest}}, Members: []Member{{Team: "team", ManifestDigest: manifestDigest, Digest: memberDigest, Path: "out", Size: int64(len(member)), Mode: 0o644}}}, nil
		}}
		cache, err := OpenRemoteCache(RemoteCacheOptions{Profile: "ci", Team: "team", Authority: Authority{CapabilityRef: "user", Principal: "reader", Repo: "gh:123"}, Repo: "gh:123", Records: r}, bytes)
		if err != nil {
			return false
		}
		hit, err := cache.Memo(t.Context(), MemoKey{Repo: "gh:123", Pipeline: "build", Job: "job", Hash: runspec.Hash(digest("key"))}, Budget{})
		if hit.Found || !errors.Is(err, ErrNotFound) {
			return false
		}
		if record, err := cache.Depcache(t.Context(), DepcacheKey{Repo: "gh:123", Name: "modules", Key: "lock"}, Budget{}); record.Digest != "" || !errors.Is(err, ErrNotFound) {
			return false
		}
		_, err = cache.Artifact(t.Context(), outputDigest)
		return errors.Is(err, ErrDigestNotReadable)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48, Rand: rand.New(rand.NewSource(591))}); err != nil {
		t.Fatal(err)
	}
}

func (r records) LookupRecord(ctx context.Context, q RecordLookup) (RecordLookupResult, error) {
	return r.lookup(ctx, q)
}

func TestRemoteCacheVerifiedDefaultAndArtifactEdges(t *testing.T) {
	d := digest("bytes")
	member := digest("artifact")
	manifestBody := fmt.Sprintf(`{"entries":[{"path":"out","digest":"%s","size":8,"mode":420}]}`, member)
	manifest := digest(manifestBody)
	key := MemoKey{Repo: "gh:123", Ref: RefScope{Ref: "feature"}, Pipeline: "build", Job: "job", Hash: runspec.Hash(digest("key"))}
	result := RecordLookupResult{Found: true, VerifiedDefault: true, Record: Record{
		Key:    RecordKey{Team: "team", Kind: KindMemo, Name: MemoRecordName, Scope: Scope{Repo: key.Repo, Ref: RefScope{Ref: "main", Verified: true}, Pipeline: key.Pipeline, Job: key.Job, Key: string(key.Hash)}},
		Digest: d, ManifestDigest: runspec.Optional[Digest]{Set: true, Value: manifest},
	}, Members: []Member{{Team: "team", ManifestDigest: manifest, Digest: member, Path: "out", Size: 8, Mode: 0o644}}}
	bodies := map[Digest]string{d: "bytes", manifest: manifestBody, member: "artifact"}
	s := &signer{download: func(q GetRequest) (SignedTransfer, error) {
		body, ok := bodies[q.Digest]
		if !ok {
			return SignedTransfer{}, ErrNotFound
		}
		return SignedTransfer{Digest: q.Digest, Size: int64(len(body))}, nil
	}}
	bytes := NewSignedStore(SignedStoreOptions{Signer: s, HTTPClient: transfer(func(_ context.Context, tr SignedTransfer, _ io.Reader) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(bodies[tr.Digest])), nil
	})})
	r := records{lookup: func(_ context.Context, q RecordLookup) (RecordLookupResult, error) {
		if q.Key.Name != MemoRecordName {
			t.Fatalf("memo lookup cannot find the committed result record: %+v", q.Key)
		}
		if !q.VerifiedDefaultOnly || q.Authority.CapabilityRef != "user-repo-read" || q.Authority.Repo != key.Repo || q.Key.Scope.Ref.Ref != "" {
			t.Fatal("lookup did not request a server-derived default branch")
		}
		return result, nil
	}}
	options := RemoteCacheOptions{Profile: "ci", Team: "team", Authority: Authority{CapabilityRef: "user-repo-read", Principal: "reader", Repo: key.Repo}, Repo: key.Repo, Records: r}
	cache, err := OpenRemoteCache(options, bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cache.Artifact(t.Context(), d); !errors.Is(err, ErrDigestNotReadable) {
		t.Fatal("arbitrary artifact was readable")
	}
	budget := Budget{Deadline: time.Now().Add(time.Minute)}
	if hit, err := cache.Memo(t.Context(), key, Budget{Deadline: time.Now().Add(-time.Second)}); !errors.Is(err, ErrBudget) || hit.Found {
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
	for _, digest := range []Digest{manifest, member} {
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
	if _, err = cache.Artifact(t.Context(), digest("foreign")); !errors.Is(err, ErrDigestNotReadable) {
		t.Fatal("unrelated artifact was readable")
	}
	for _, mutation := range []string{"manual", "false_default", "foreign_team", "foreign_repo", "foreign_pipeline", "foreign_job", "foreign_hash", "foreign_name", "empty_ref", "foreign_member", "member_size", "member_path", "member_mode", "manifest_members", "corrupt"} {
		t.Run(mutation, func(t *testing.T) {
			bad := result
			bad.Members = append([]Member(nil), result.Members...)
			switch mutation {
			case "manual":
				bad.Record.Key.Scope.Ref.Verified = false
			case "false_default":
				bad.VerifiedDefault = false
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
			case "empty_ref":
				bad.Record.Key.Scope.Ref.Ref = ""
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
			options.Records = records{lookup: func(context.Context, RecordLookup) (RecordLookupResult, error) { return bad, nil }}
			isolated, err := OpenRemoteCache(options, bytes)
			if err != nil {
				t.Fatal(err)
			}
			hit, err := isolated.Memo(t.Context(), key, budget)
			if err == nil || hit.Found {
				t.Fatalf("unsafe memo hit: %+v, %v", hit, err)
			}
			if _, err := isolated.Artifact(t.Context(), d); !errors.Is(err, ErrDigestNotReadable) {
				t.Fatal("rejected record granted artifact access")
			}
		})
	}
}

func TestRemoteCacheRequiresOptIn(t *testing.T) {
	if _, err := OpenRemoteCache(RemoteCacheOptions{}, nil); err == nil {
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
			bytes := NewSignedStore(SignedStoreOptions{
				Signer: &signer{transfer: SignedTransfer{Digest: d, Size: int64(len(output))}},
				HTTPClient: transfer(func(context.Context, SignedTransfer, io.Reader) (io.ReadCloser, error) {
					if fault == "read_cancelled" {
						cancel()
					}
					return body, nil
				}),
			})
			records := records{lookup: func(_ context.Context, q RecordLookup) (RecordLookupResult, error) {
				if fault == "lookup_cancelled" {
					cancel()
					return RecordLookupResult{}, ctx.Err()
				}
				key := q.Key
				key.Team, key.Scope.Ref = "team", RefScope{Ref: "main", Verified: true}
				return RecordLookupResult{Found: true, VerifiedDefault: true, Record: Record{Key: key, Digest: d}}, nil
			}}
			cache, err := OpenRemoteCache(RemoteCacheOptions{Profile: "ci", Team: "team", Authority: Authority{CapabilityRef: "user", Principal: "reader", Repo: "gh:123"}, Repo: "gh:123", Records: records}, bytes)
			if err != nil {
				t.Fatal(err)
			}
			hit, err := cache.Memo(ctx, MemoKey{Repo: "gh:123", Ref: RefScope{Ref: "feature"}, Pipeline: "build", Job: "job", Hash: runspec.Hash(digest("key"))}, Budget{})
			if hit.Found || err == nil || (ctx.Err() != nil && !errors.Is(err, context.Canceled)) {
				t.Fatalf("failed memo = %+v, %v", hit, err)
			}
			if fault != "lookup_cancelled" && !body.closed {
				t.Fatal("download was not closed")
			}
			if _, err := cache.Artifact(t.Context(), d); !errors.Is(err, ErrDigestNotReadable) {
				t.Fatalf("failed memo granted artifacts: %v", err)
			}
		})
	}
}

func TestRemoteDepcacheArchiveIsReadableAfterTrustedLookup(t *testing.T) {
	d := digest("archive")
	s := &signer{transfer: SignedTransfer{Digest: d, Size: 7}}
	bytes := NewSignedStore(SignedStoreOptions{Signer: s, HTTPClient: transfer(func(context.Context, SignedTransfer, io.Reader) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("archive")), nil
	})})
	r := records{lookup: func(_ context.Context, q RecordLookup) (RecordLookupResult, error) {
		if !q.VerifiedDefaultOnly || q.Key.Kind != KindDepcache {
			t.Fatal("unscoped depcache lookup")
		}
		key := q.Key
		key.Team = "team"
		key.Scope.Ref = RefScope{Ref: "main", Verified: true}
		return RecordLookupResult{Found: true, VerifiedDefault: true, Record: Record{Key: key, Digest: d}}, nil
	}}
	cache, err := OpenRemoteCache(RemoteCacheOptions{Profile: "ci", Team: "team", Authority: Authority{CapabilityRef: "user", Principal: "reader", Repo: "gh:123"}, Repo: "gh:123", Records: r}, bytes)
	if err != nil {
		t.Fatal(err)
	}
	key := DepcacheKey{Repo: "gh:123", Ref: RefScope{Ref: "feature"}, Name: "modules", Key: "lock-hash"}
	record, err := cache.Depcache(t.Context(), key, Budget{Deadline: time.Now().Add(time.Minute)})
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
	owner.Kind = UploadOwnerUnit
	owner.Attempt = runspec.Optional[runspec.AttemptIdentity]{}
	owner.Unit = runspec.Optional[UploadUnit]{Set: true, Value: UploadUnit{Kind: UploadPlanUnit, ID: "plan", Generation: 3}}
	a := Authority{Principal: owner.Principal, CapabilityRef: owner.CapabilityRef, Upload: runspec.Optional[UploadIdentity]{Set: true, Value: owner}}
	for _, kind := range []Kind{KindBinary, KindSourceBundle} {
		if err := AuthorizeUpload(a, owner, kind); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []Kind{KindOutput, KindManifest, KindMemo, KindDepcache} {
		if err := AuthorizeUpload(a, owner, kind); !errors.Is(err, ErrDigestNotReadable) {
			t.Fatalf("owner uploaded %s: %v", kind, err)
		}
	}
	bad := owner
	bad.Unit.Value.Generation++
	if err := AuthorizeUpload(a, bad, KindBinary); !errors.Is(err, ErrDigestNotReadable) {
		t.Fatal("owner fence transferred")
	}
	job := identity()
	edge := EdgeRequest{TargetKind: KindOutput, Digest: digest("bytes"), Authority: Authority{Principal: job.Principal, CapabilityRef: job.CapabilityRef, Attempt: job.Attempt}, OwnReceipt: runspec.Optional[Receipt]{Set: true, Value: Receipt{Identity: owner, Digest: digest("bytes")}}}
	if err := AuthorizeEdge(edge); !errors.Is(err, ErrDigestNotReadable) {
		t.Fatal("owner receipt became job authority")
	}
	published := job
	published.Kind = UploadPublication
	a = Authority{Principal: published.Principal, CapabilityRef: published.CapabilityRef, Attempt: published.Attempt, Upload: runspec.Optional[UploadIdentity]{Set: true, Value: published}}
	if err := AuthorizeUpload(a, published, KindOutput); err != nil {
		t.Fatal(err)
	}
	if err := AuthorizeUpload(a, published, KindMemo); !errors.Is(err, ErrDigestNotReadable) {
		t.Fatal("publication gained memo write")
	}
	bad = published
	bad.Attempt.Value.NodeID = "other"
	if err := AuthorizeUpload(a, bad, KindOutput); !errors.Is(err, ErrDigestNotReadable) {
		t.Fatal("publication gained another job")
	}
}
