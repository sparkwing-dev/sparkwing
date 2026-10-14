package blob_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func digest(body string) blob.Digest {
	return blob.Digest(fmt.Sprintf("%x", sha256.Sum256([]byte(body))))
}

func identity() blob.UploadIdentity {
	a := runspec.AttemptIdentity{Team: "team", RunID: "run", NodeID: "job", Attempt: 1, Generation: 2}
	return blob.UploadIdentity{Kind: blob.UploadJob, Team: a.Team, RunID: a.RunID, Principal: "principal", CapabilityRef: "cap", Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: a}}
}

type signer struct {
	intent   blob.UploadIntent
	commits  int
	transfer blob.SignedTransfer
	download func(blob.GetRequest) (blob.SignedTransfer, error)
	has      func(context.Context, blob.HasManyRequest) (blob.Presence, error)
}

func (s *signer) Upload(_ context.Context, i blob.UploadIntent) (blob.UploadTicket, error) {
	s.intent = i
	return blob.UploadTicket{ID: "ticket", Transfer: blob.SignedTransfer{Digest: i.Digest, Size: i.Size}}, nil
}
func (s *signer) Commit(_ context.Context, _ blob.CommitUpload) (blob.Receipt, error) {
	s.commits++
	return blob.Receipt{Identity: s.intent.Identity, Digest: s.intent.Digest}, nil
}
func (s *signer) Download(_ context.Context, request blob.GetRequest) (blob.SignedTransfer, error) {
	if s.download != nil {
		return s.download(request)
	}
	return s.transfer, nil
}
func (s *signer) HasMany(ctx context.Context, req blob.HasManyRequest) (blob.Presence, error) {
	return s.has(ctx, req)
}

type transfer func(context.Context, blob.SignedTransfer, io.Reader) (io.ReadCloser, error)

func (f transfer) Transfer(ctx context.Context, s blob.SignedTransfer, body io.Reader) (io.ReadCloser, error) {
	return f(ctx, s, body)
}

func TestSignedPutVerifiesBeforeCommit(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		size       int64
		consume    bool
		want       bool
	}{
		{"intact", "hello", 5, true, true},
		{"corrupt", "jello", 5, true, false},
		{"short", "hell", 5, true, false},
		{"long", "hello!", 5, true, false},
		{"unconsumed", "hello", 5, false, false},
		{"empty", "", 0, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &signer{}
			client := transfer(func(_ context.Context, _ blob.SignedTransfer, body io.Reader) (io.ReadCloser, error) {
				if tc.consume {
					if _, err := io.Copy(io.Discard, body); err != nil {
						return nil, err
					}
				}
				return io.NopCloser(strings.NewReader("")), nil
			})
			wantDigest := digest("hello")
			if tc.name == "empty" {
				wantDigest = digest("")
			}
			store := blob.NewSignedStore(blob.SignedStoreOptions{Signer: s, HTTPClient: client})
			i := identity()
			_, err := store.Put(t.Context(), blob.PutRequest{Identity: i, Authority: blob.Authority{CapabilityRef: i.CapabilityRef, Principal: i.Principal, Attempt: i.Attempt}, Digest: wantDigest, Size: tc.size, Body: strings.NewReader(tc.body)})
			if (err == nil) != tc.want {
				t.Fatalf("Put = %v, success %v", err, tc.want)
			}
			if !tc.want && s.commits != 0 {
				t.Fatal("unverified upload committed")
			}
			if tc.want && s.commits != 1 {
				t.Fatal("verified upload was not committed once")
			}
		})
	}
}

func TestDownloadRequiresVerifiedEOF(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		early      bool
		want       bool
	}{
		{"intact", "hello", false, true}, {"equal size corruption", "jello", false, false},
		{"truncated", "hell", false, false}, {"oversize", "hello!", false, false},
		{"early close", "hello", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &signer{transfer: blob.SignedTransfer{Digest: digest("hello"), Size: 5}}
			store := blob.NewSignedStore(blob.SignedStoreOptions{Signer: s, HTTPClient: transfer(func(context.Context, blob.SignedTransfer, io.Reader) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader(tc.body)), nil
			})})
			r, err := store.Get(t.Context(), blob.GetRequest{Digest: digest("hello")})
			if err != nil {
				t.Fatal(err)
			}
			if tc.early {
				b := make([]byte, 5)
				if _, err := io.ReadFull(r, b); err != nil {
					t.Fatal(err)
				}
			} else {
				_, err = io.ReadAll(r)
				if (err == nil) != tc.want {
					t.Fatalf("read = %v", err)
				}
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			if r.Verified() != tc.want {
				t.Fatalf("Verified=%v, want %v", r.Verified(), tc.want)
			}
		})
	}
}

func TestHasManyBudget(t *testing.T) {
	s := &signer{has: func(ctx context.Context, _ blob.HasManyRequest) (blob.Presence, error) {
		<-ctx.Done()
		return blob.Presence{}, ctx.Err()
	}}
	store := blob.NewSignedStore(blob.SignedStoreOptions{Signer: s})
	_, err := store.HasMany(t.Context(), blob.HasManyRequest{Digests: []blob.Digest{digest("hello")}, Budget: blob.Budget{Deadline: time.Now().Add(-time.Second)}})
	if !errors.Is(err, blob.ErrBudget) {
		t.Fatalf("HasMany=%v", err)
	}
	s.has = func(ctx context.Context, req blob.HasManyRequest) (blob.Presence, error) {
		if deadline, ok := ctx.Deadline(); !ok || deadline.After(req.Budget.Deadline) {
			t.Fatal("lookup escaped its caller's budget")
		}
		return blob.Presence{Present: req.Digests[:1]}, context.DeadlineExceeded
	}
	partial, err := store.HasMany(t.Context(), blob.HasManyRequest{Digests: []blob.Digest{digest("hello"), digest("world")}, Budget: blob.Budget{Deadline: time.Now().Add(time.Minute)}})
	if !errors.Is(err, blob.ErrBudget) || len(partial.Present) != 0 {
		t.Fatalf("partial timed-out lookup=%+v %v", partial, err)
	}
}

func TestHasManyLargeAndDuplicate(t *testing.T) {
	var ds []blob.Digest
	var want []blob.Digest
	for i := 0; i < 4097; i++ {
		d := digest(fmt.Sprint(i))
		ds = append(ds, d, d)
		if i%2 == 0 {
			want = append(want, d)
		}
	}
	s := &signer{has: func(_ context.Context, req blob.HasManyRequest) (blob.Presence, error) {
		if len(req.Digests) != 4097 {
			t.Fatalf("sent %d unique digests", len(req.Digests))
		}
		return blob.Presence{Present: want}, nil
	}}
	got, err := blob.NewSignedStore(blob.SignedStoreOptions{Signer: s}).HasMany(t.Context(), blob.HasManyRequest{Digests: ds, Budget: blob.Budget{Deadline: time.Now().Add(time.Minute)}})
	if err != nil || !slices.Equal(got.Present, want) {
		t.Fatalf("HasMany=%d, %v", len(got.Present), err)
	}
}
