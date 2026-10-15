package blob_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
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
	upload   func(context.Context, blob.UploadIntent) (blob.UploadTicket, error)
	commit   func(context.Context, blob.CommitUpload) (blob.Receipt, error)
	download func(blob.GetRequest) (blob.SignedTransfer, error)
	has      func(context.Context, blob.HasManyRequest) (blob.Presence, error)
}

func (s *signer) Upload(ctx context.Context, i blob.UploadIntent) (blob.UploadTicket, error) {
	s.intent = i
	if s.upload != nil {
		return s.upload(ctx, i)
	}
	return blob.UploadTicket{ID: "ticket", Transfer: blob.SignedTransfer{Digest: i.Digest, Size: i.Size}}, nil
}

func (s *signer) Commit(ctx context.Context, request blob.CommitUpload) (blob.Receipt, error) {
	s.commits++
	if s.commit != nil {
		return s.commit(ctx, request)
	}
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
		{"intact", "hello", false, true},
		{"equal size corruption", "jello", false, false},
		{"truncated", "hell", false, false},
		{"oversize", "hello!", false, false},
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

func TestCommitAfterInterruptedUpload(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	backend, err := blob.NewFilesystem(blob.FilesystemOptions{
		Directory: dir, Endpoint: "https://objects.invalid/data",
		SigningKey: []byte(strings.Repeat("x", 32)), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	settlementLost := errors.New("settlement did not commit")
	i := identity()
	object := blob.Blob{Team: i.Team, Digest: digest("hello"), Size: 5}
	var ticket, pending string
	var uploads, receipts int
	s := &signer{}
	s.upload = func(ctx context.Context, intent blob.UploadIntent) (blob.UploadTicket, error) {
		uploads++
		ticket = fmt.Sprintf("ticket-%d", uploads)
		pending = "teams/team/pending/" + ticket
		signed, err := backend.Sign(ctx, capability.ObjectToSign{
			Method: http.MethodPut, Key: pending, Size: intent.Size,
			SHA256: string(intent.Digest), ExpiresAt: now.Add(time.Minute),
		})
		return blob.UploadTicket{ID: ticket, Transfer: blob.SignedTransfer{
			URL: signed.URL, Digest: intent.Digest, Size: intent.Size, ExpiresAt: signed.ExpiresAt,
		}}, err
	}
	s.commit = func(ctx context.Context, request blob.CommitUpload) (blob.Receipt, error) {
		if request.TicketID != ticket || request.Authority.Principal != i.Principal {
			t.Fatal("commit lost the current ticket or principal")
		}
		if err := backend.CommitObject(ctx, pending, object); err != nil {
			return blob.Receipt{}, err
		}
		if uploads == 1 {
			return blob.Receipt{}, settlementLost
		}
		receipts++
		return blob.Receipt{Identity: i, Digest: object.Digest}, nil
	}
	bytes := blob.NewSignedStore(blob.SignedStoreOptions{Signer: s, HTTPClient: blob.TransferClient{
		Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
			response := httptest.NewRecorder()
			backend.ServeHTTP(response, request)
			return response.Result(), nil
		}),
	}})
	put := func() (blob.Receipt, error) {
		return bytes.Put(t.Context(), blob.PutRequest{
			Identity: i, Authority: blob.Authority{CapabilityRef: i.CapabilityRef, Principal: i.Principal, Attempt: i.Attempt},
			Digest: object.Digest, Size: object.Size, Body: strings.NewReader("hello"),
		})
	}
	if receipt, err := put(); !errors.Is(err, settlementLost) || receipt != (blob.Receipt{}) || receipts != 0 {
		t.Fatalf("interrupted commit = %+v, %v, receipts %d", receipt, err, receipts)
	}
	if err := backend.DeleteObject(t.Context(), pending); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	i.Principal, i.CapabilityRef = "new-principal", "new-capability"
	if receipt, err := put(); err != nil || receipt.Identity != i || receipts != 1 || uploads != 2 {
		t.Fatalf("new uploader retry = %+v, %v, uploads/receipts %d/%d", receipt, err, uploads, receipts)
	}
	final, err := blob.ObjectKey(object.Team, object.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(final)), []byte("jello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if receipt, err := put(); !errors.Is(err, blob.ErrIntegrity) || receipt != (blob.Receipt{}) || receipts != 1 {
		t.Fatalf("corrupt existing object = %+v, %v, receipts %d", receipt, err, receipts)
	}
}
