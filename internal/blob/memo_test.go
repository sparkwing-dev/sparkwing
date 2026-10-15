package blob_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type memoRecords struct {
	blob.RecordStore
	copy func(context.Context, blob.CopyMemoRequest) (blob.MemoHit, error)
}

func (r memoRecords) CopyMemo(ctx context.Context, q blob.CopyMemoRequest) (blob.MemoHit, error) {
	return r.copy(ctx, q)
}

func TestMemoMaterializationFailures(t *testing.T) {
	for _, failure := range []string{"none", "empty_output", "missing_output_record", "follower_output", "manifest_commit", "missing_bytes", "refused_edge", "corrupt_output", "corrupt_member", "member_size", "manifest_members", "budget"} {
		t.Run(failure, func(t *testing.T) {
			i := identity()
			a := blob.Authority{CapabilityRef: i.CapabilityRef, Principal: i.Principal, Attempt: i.Attempt}
			output, member := digest(`"result"`), digest("file")
			manifestBody := fmt.Sprintf(`{"entries":[{"path":"file","digest":"%s","size":4,"mode":420}]}`, member)
			if failure == "manifest_members" {
				manifestBody = `{"entries":[]}`
			}
			manifest := digest(manifestBody)
			hit := blob.MemoHit{
				Found:    true,
				Output:   runspec.Optional[blob.Digest]{Set: true, Value: output},
				Manifest: runspec.Optional[blob.Digest]{Set: true, Value: manifest},
				Members:  []blob.Member{{Team: i.Team, ManifestDigest: manifest, Digest: member, Path: "file", Size: 4, Mode: 0o644}},
			}
			bodies := map[blob.Digest]string{output: `"result"`, manifest: manifestBody, member: "file"}
			sizes := map[blob.Digest]int64{}
			for d, body := range bodies {
				sizes[d] = int64(len(body))
			}
			deadline := time.Now().Add(time.Minute)
			copyCalls := 0
			records := memoRecords{copy: func(ctx context.Context, q blob.CopyMemoRequest) (blob.MemoHit, error) {
				copyCalls++
				if got, ok := ctx.Deadline(); !ok || !got.Equal(deadline) {
					t.Fatal("copy has no lookup deadline")
				}
				if q.Lookup.Authority != a || q.Destination != i.Attempt.Value {
					t.Fatal("lost reader binding")
				}
				if failure == "manifest_commit" {
					return blob.MemoHit{}, errors.New("refused manifest commit")
				}
				if failure == "refused_edge" {
					return blob.MemoHit{}, blob.ErrDigestNotReadable
				}
				return hit, nil
			}}
			switch failure {
			case "empty_output":
				hit.Output.Value = digest("")
				bodies[hit.Output.Value], sizes[hit.Output.Value] = "", 0
			case "missing_output_record":
				hit.Output = runspec.Optional[blob.Digest]{}
			case "follower_output":
				delete(bodies, output)
			case "missing_bytes":
				delete(bodies, member)
			case "corrupt_output":
				bodies[output] = `"wrong!"`
			case "corrupt_member":
				bodies[member] = "evil"
			case "member_size":
				hit.Members[0].Size++
			case "budget":
				deadline = time.Time{}
			}
			gets := 0
			s := &signer{download: func(q blob.GetRequest) (blob.SignedTransfer, error) {
				gets++
				if q.Authority != a {
					t.Fatal("lost download authority")
				}
				if _, ok := bodies[q.Digest]; !ok {
					return blob.SignedTransfer{}, blob.ErrNotFound
				}
				return blob.SignedTransfer{Digest: q.Digest, Size: sizes[q.Digest]}, nil
			}}
			bytes := blob.NewSignedStore(blob.SignedStoreOptions{Signer: s, HTTPClient: transfer(func(_ context.Context, q blob.SignedTransfer, _ io.Reader) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader(bodies[q.Digest])), nil
			})})
			got, err := blob.MaterializeMemo(t.Context(), bytes, records, blob.CopyMemoRequest{Lookup: blob.MemoLookup{Authority: a, Budget: blob.Budget{Deadline: deadline}}, Destination: i.Attempt.Value})
			if failure == "refused_edge" {
				if !errors.Is(err, blob.ErrDigestNotReadable) {
					t.Fatalf("authority refusal became a miss: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if failure == "none" || failure == "empty_output" {
				want := `"result"`
				if failure == "empty_output" {
					want = ""
				}
				if !got.Hit.Found || string(got.Output) != want || gets != 3 {
					t.Fatalf("materialized %+v with %d reads", got, gets)
				}
			} else if got.Hit.Found || len(got.Output) != 0 {
				t.Fatal("failed materialization became a hit")
			}
			if failure == "budget" && copyCalls != 0 {
				t.Fatal("unbounded copy attempted")
			}
		})
	}
}

func TestMemoMaterializationCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := blob.MaterializeMemo(ctx, nil, nil, blob.CopyMemoRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func FuzzMemoAuthorityFailuresStayErrors(f *testing.F) {
	for stage := uint8(0); stage < 4; stage++ {
		for kind := uint8(0); kind < 4; kind++ {
			f.Add(stage, kind, "authority refused")
		}
	}
	f.Fuzz(func(t *testing.T, stage, kind uint8, message string) {
		var refusal error = blob.ErrDigestNotReadable
		if kind%4 != 0 {
			code := []apierr.Code{apierr.Forbidden, apierr.Unauthenticated, apierr.LockHeld}[(kind-1)%3]
			refusal = &apierr.Error{Code: code, Message: message}
		}
		wrapped := fmt.Errorf("memo: %w", refusal)
		output, file := digest(`"result"`), digest("file")
		body := fmt.Sprintf(`{"entries":[{"path":"file","digest":"%s","size":4,"mode":420}]}`, file)
		manifest := digest(body)
		hit := blob.MemoHit{
			Found:    true,
			Output:   runspec.Optional[blob.Digest]{Set: true, Value: output},
			Manifest: runspec.Optional[blob.Digest]{Set: true, Value: manifest},
			Members:  []blob.Member{{Team: "team", ManifestDigest: manifest, Digest: file, Path: "file", Size: 4, Mode: 420}},
		}
		records := memoRecords{copy: func(context.Context, blob.CopyMemoRequest) (blob.MemoHit, error) {
			if stage%4 == 0 {
				return blob.MemoHit{}, wrapped
			}
			return hit, nil
		}}
		bodies := map[blob.Digest]string{output: `"result"`, manifest: body, file: "file"}
		target := []blob.Digest{output, output, manifest, file}[stage%4]
		s := &signer{download: func(q blob.GetRequest) (blob.SignedTransfer, error) {
			if q.Digest == target {
				return blob.SignedTransfer{}, wrapped
			}
			return blob.SignedTransfer{Digest: q.Digest, Size: int64(len(bodies[q.Digest]))}, nil
		}}
		bytes := blob.NewSignedStore(blob.SignedStoreOptions{Signer: s, HTTPClient: transfer(func(_ context.Context, q blob.SignedTransfer, _ io.Reader) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(bodies[q.Digest])), nil
		})})
		got, err := blob.MaterializeMemo(t.Context(), bytes, records, blob.CopyMemoRequest{Lookup: blob.MemoLookup{Budget: blob.Budget{Deadline: time.Now().Add(time.Minute)}}, Destination: identity().Attempt.Value})
		if !errors.Is(err, refusal) || got.Hit.Found || len(got.Output) != 0 {
			t.Fatalf("authority failure became result %+v, error %v; want %v", got, err, refusal)
		}
	})
}
