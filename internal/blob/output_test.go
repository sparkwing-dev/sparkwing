package blob_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type outputRecords struct {
	blob.RecordStore
	request blob.CommitRecordRequest
	lookup  blob.RecordLookup
	result  blob.RecordLookupResult
	err     error
}

func (r *outputRecords) CommitRecord(_ context.Context, q blob.CommitRecordRequest) (blob.Record, error) {
	r.request = q
	if r.err != nil {
		return blob.Record{}, r.err
	}
	r.result = blob.RecordLookupResult{Found: true, Record: blob.Record{Key: q.Key, Digest: q.Digest, Source: q.Source}}
	return r.result.Record, nil
}

func (r *outputRecords) LookupRecord(_ context.Context, q blob.RecordLookup) (blob.RecordLookupResult, error) {
	r.lookup = q
	return r.result, r.err
}

func newOutputData(t *testing.T) (blob.OutputData, *outputRecords, *signer) {
	t.Helper()
	i := identity()
	objects := make(map[blob.Digest]string)
	s := &signer{download: func(q blob.GetRequest) (blob.SignedTransfer, error) {
		body, ok := objects[q.Digest]
		if !ok {
			return blob.SignedTransfer{}, blob.ErrNotFound
		}
		return blob.SignedTransfer{Digest: q.Digest, Size: int64(len(body))}, nil
	}}
	store := blob.NewSignedStore(blob.SignedStoreOptions{Signer: s, HTTPClient: transfer(func(_ context.Context, q blob.SignedTransfer, body io.Reader) (io.ReadCloser, error) {
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
	return blob.OutputData{
		Bytes: store, Records: r, Identity: i, Pipeline: "build", Budget: blob.Budget{Deadline: time.Now().Add(time.Minute)},
		Authority: blob.Authority{CapabilityRef: i.CapabilityRef, Principal: i.Principal, Attempt: i.Attempt, Repo: "gh:123", Ref: blob.RefFacts{Scope: blob.RefScope{Ref: "feature"}}},
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
			if key.Kind != blob.KindOutput || key.Team != data.Identity.Team || key.Name != data.Identity.RunID || key.Scope.Job != data.Identity.Attempt.Value.NodeID || key.Scope.Pipeline != data.Pipeline || key.Scope.Repo != data.Authority.Repo || key.Scope.Ref != data.Authority.Ref.Scope || key.Scope.Key != "" {
				t.Fatalf("output key = %+v", key)
			}
			if record.Digest != digest(body) || signer.commits != 1 || records.request.Source != data.Identity || records.request.Authority != data.Authority || records.request.ManifestDigest.Set || len(records.request.Members) != 0 {
				t.Fatalf("output commit = %+v", records.request)
			}
			got, err := data.Read(t.Context(), key)
			if err != nil || string(got) != body || records.lookup.Authority != data.Authority || records.lookup.Key != key || records.lookup.Budget != data.Budget {
				t.Fatalf("output read = %q, %v", got, err)
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
				data.Identity.Kind = blob.UploadOwnerUnit
				data.Identity.Attempt = runspec.Optional[runspec.AttemptIdentity]{}
				data.Identity.Unit = runspec.Optional[blob.UploadUnit]{Set: true, Value: blob.UploadUnit{Kind: blob.UploadPlanUnit, ID: "plan", Generation: 3}}
				data.Authority.Attempt = runspec.Optional[runspec.AttemptIdentity]{}
				data.Authority.Upload = runspec.Optional[blob.UploadIdentity]{Set: true, Value: data.Identity}
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
	data.Identity.Kind = blob.UploadPublication
	data.Authority.Upload = runspec.Optional[blob.UploadIdentity]{Set: true, Value: data.Identity}
	record, err := data.Put(t.Context(), []byte(`{"published":true}`))
	if err != nil || record.Source != data.Identity || records.request.Source.Kind != blob.UploadPublication || signer.intent.Identity != data.Identity {
		t.Fatalf("imported output = %+v, %v", record, err)
	}
}

func TestOutputReadRefusals(t *testing.T) {
	for _, fault := range []string{"missing_record", "refused_edge", "wrong_key", "wrong_kind", "missing_bytes", "corrupt", "short", "long", "output_limit", "unverified", "close", "budget", "cancelled"} {
		t.Run(fault, func(t *testing.T) {
			data, records, _ := newOutputData(t)
			record, err := data.Put(t.Context(), []byte("result"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			key := record.Key
			body := &remoteBody{Reader: strings.NewReader("result")}
			size := int64(6)
			downloads := 0
			switch fault {
			case "missing_record":
				records.result.Found = false
			case "refused_edge":
				records.err = blob.ErrDigestNotReadable
			case "wrong_key":
				records.result.Record.Key.Name = "other-run"
			case "wrong_kind":
				key.Kind = blob.KindDepcache
			case "corrupt":
				body.Reader = strings.NewReader("wrong!")
			case "short":
				body.Reader = strings.NewReader("resul")
			case "long":
				body.Reader = strings.NewReader("result!")
			case "output_limit":
				raw := strings.Repeat("x", int(limits.NodeOutputBytes)+1)
				body.Reader, size = strings.NewReader(raw), int64(len(raw))
				records.result.Record.Digest = digest(raw)
			case "close":
				body.err = errors.New("close failed")
			case "budget":
				data.Budget.Deadline = time.Time{}
			case "cancelled":
				cancel()
			}
			data.Bytes = blob.NewSignedStore(blob.SignedStoreOptions{
				Signer: &signer{download: func(q blob.GetRequest) (blob.SignedTransfer, error) {
					downloads++
					if fault == "missing_bytes" {
						return blob.SignedTransfer{}, blob.ErrNotFound
					}
					return blob.SignedTransfer{Digest: q.Digest, Size: size}, nil
				}},
				HTTPClient: transfer(func(context.Context, blob.SignedTransfer, io.Reader) (io.ReadCloser, error) { return body, nil }),
			})
			if fault == "unverified" {
				data.Bytes = unverifiedOutputStore{Store: data.Bytes, body: body}
			}
			got, err := data.Read(ctx, key)
			if err == nil || got != nil {
				t.Fatalf("bad output returned bytes: %q, %v", got, err)
			}
			if fault == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation = %v", err)
			}
			if downloads > 0 && fault != "missing_bytes" && !body.closed {
				t.Fatal("failed output read leaked its stream")
			}
		})
	}
}

type unverifiedOutputStore struct {
	blob.Store
	body *remoteBody
}

func (s unverifiedOutputStore) Get(context.Context, blob.GetRequest) (blob.VerifiedReader, error) {
	return unverifiedOutput{s.body}, nil
}

type unverifiedOutput struct{ *remoteBody }

func (unverifiedOutput) Verified() bool { return false }
