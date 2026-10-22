package nodehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

var outputRefused = errors.New("output backend refused")

func outputDigest(value []byte) blob.Digest {
	sum := sha256.Sum256(value)
	return blob.Digest(hex.EncodeToString(sum[:]))
}

type outputReader struct {
	*bytes.Reader
	mode   string
	eof    bool
	closed bool
}

func (r *outputReader) Read(dst []byte) (int, error) {
	n, err := r.Reader.Read(dst)
	if err == io.EOF {
		r.eof = true
		if r.mode == "corrupt" {
			err = blob.ErrIntegrity
		}
	}
	return n, err
}

func (r *outputReader) Verified() bool { return r.eof && r.mode != "unverified" && r.mode != "corrupt" }
func (r *outputReader) Close() error {
	r.closed = true
	if r.mode == "close" {
		return outputRefused
	}
	return nil
}

func outputFixture(t *testing.T, mode string) (NodeInput, OutputRecords, *outputBackendFixture, *executionHostFixtureState) {
	t.Helper()
	var trace []string
	var processes []*scriptedSDK
	input, st, _ := executionHostFixture(t, "output_refused", &trace, &processes)
	var spec runspec.Spec
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Request.Request.Team = "team"
	spec.Request.RunID = "source-run"
	input.Accepted.Spec, _ = json.Marshal(spec)
	st.snapshot.Run.Spec = &spec
	input.Entry = &source.Entry{Engine: input.Engine}
	input.Attempt++
	attempt := runspec.AttemptIdentity{Team: "team", RunID: input.Accepted.RunID, NodeID: input.NodeID, Generation: int64(input.Generation), Attempt: int64(input.Attempt)}
	identity := blob.UploadIdentity{Kind: blob.UploadJob, Team: "team", RunID: input.Accepted.RunID, Principal: "principal", CapabilityRef: "live-claim", Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: attempt}}
	upload := JobUpload{Identity: identity, Authority: blob.Authority{CapabilityRef: identity.CapabilityRef, Principal: identity.Principal, Attempt: identity.Attempt, Repo: "example.com/acme/repo"}, Key: blob.ResultRecordKey(attempt, "example.com/acme/repo", blob.RefScope{}, "build", blob.KindOutput)}
	b := newOutputBackend(mode, upload, []byte(`{"value":7}`))
	adapter := OutputRecords{Blobs: b, Records: b, Bind: func(_ context.Context, got NodeInput) (JobUpload, error) {
		if got.Accepted.RunID != attempt.RunID || got.Authority != input.Authority || got.Generation != runspec.ClaimGeneration(attempt.Generation) || got.Attempt != runspec.AttemptOrdinal(attempt.Attempt) {
			return JobUpload{}, errors.New("binding lost the awarded claim")
		}
		if mode == "bind" {
			return JobUpload{}, outputRefused
		}
		return b.upload, nil
	}}
	return input, adapter, b, st
}

func TestOutputRecordCommit(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"success", "bound_storage", "null", "at_limit", "invalid", "invalid_seal", "oversized", "bind", "foreign_attempt", "foreign_generation", "foreign_run", "owner_receipt", "foreign_key", "foreign_layout", "foreign_name", "foreign_ref", "foreign_authority", "foreign_principal", "cancelled", "put", "receipt_digest", "receipt_attempt", "commit", "record_key", "record_digest", "record_attempt"} {
		t.Run(mode, func(t *testing.T) {
			input, adapter, b, _ := outputFixture(t, mode)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "bound_storage":
				input.Storage = StorageBinding{Bytes: b, Records: b, Authority: b.upload.Authority}
				adapter.Blobs, adapter.Records = nil, nil
			case "null":
				b.value = []byte("null")
			case "invalid":
				b.value = []byte("invalid JSON")
			case "invalid_seal":
				input.Accepted.SpecHash = "changed"
			case "oversized":
				b.value = []byte(`"` + strings.Repeat("x", int(limits.NodeOutputBytes)) + `"`)
			case "at_limit":
				b.value = []byte(`"` + strings.Repeat("x", int(limits.NodeOutputBytes)-2) + `"`)
			case "foreign_attempt":
				b.upload.Identity.Attempt.Value.Attempt++
			case "foreign_generation":
				b.upload.Identity.Attempt.Value.Generation++
			case "foreign_run":
				b.upload.Identity.RunID = "source-run"
			case "owner_receipt":
				b.upload.Identity.Kind = blob.UploadOwnerUnit
			case "foreign_key":
				b.upload.Key.Scope.Job = "foreign"
			case "foreign_layout":
				b.upload.Key.Scope.Key = "foreign"
			case "foreign_name":
				b.upload.Key.Name = "foreign"
			case "foreign_ref":
				b.upload.Key.Scope.Ref.Verified = true
			case "foreign_authority":
				b.upload.Authority.Attempt.Value.NodeID = "foreign"
			case "foreign_principal":
				b.upload.Authority.Principal = "foreign"
			case "cancelled":
				cancel()
			}
			key, err := adapter.PublishOutput(ctx, input, b.value)
			ok := mode == "success" || mode == "bound_storage" || mode == "null" || mode == "at_limit"
			if ok {
				if err != nil || key == nil || *key != b.upload.Key || b.commit == nil || b.commit.Source != b.upload.Identity || b.commit.Authority != b.upload.Authority || b.commit.Digest != outputDigest(b.value) {
					t.Fatalf("key=%v commit=%+v error=%v", key, b.commit, err)
				}
			} else if err == nil || key != nil {
				t.Fatalf("refused output returned key=%v error=%v", key, err)
			}
			if (mode == "bind" || mode == "put" || mode == "commit") && !errors.Is(err, outputRefused) {
				t.Fatalf("lost backend refusal: %v", err)
			}
			if mode == "cancelled" && (!errors.Is(err, context.Canceled) || b.put != nil) {
				t.Fatalf("cancelled output reached upload: %v", err)
			}
			if (strings.HasPrefix(mode, "receipt_") || mode == "put") && b.commit != nil {
				t.Fatal("unverified upload reached record commit")
			}
			if (strings.HasPrefix(mode, "foreign_") || mode == "owner_receipt" || strings.HasPrefix(mode, "invalid") || mode == "oversized") && b.put != nil {
				t.Fatal("invalid output or binding reached upload")
			}
		})
	}
}

func TestOutputRecordRead(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"success", "null", "at_limit", "invalid", "oversized", "lookup", "missing_record", "lookup_key", "missing_bytes", "forbidden", "corrupt", "unverified", "close"} {
		t.Run(mode, func(t *testing.T) {
			_, adapter, b, _ := outputFixture(t, mode)
			if mode == "null" {
				b.value = []byte("null")
			}
			if mode == "invalid" {
				b.value = []byte("bad JSON")
			}
			if mode == "oversized" {
				b.value = []byte(`"` + strings.Repeat("x", int(limits.NodeOutputBytes)-1) + `"`)
			}
			if mode == "at_limit" {
				b.value = []byte(`"` + strings.Repeat("x", int(limits.NodeOutputBytes)-2) + `"`)
			}
			request := blob.RecordLookup{Authority: b.upload.Authority, Key: b.upload.Key}
			value, err := adapter.ReadOutput(t.Context(), request)
			ok := mode == "success" || mode == "null" || mode == "at_limit"
			if ok {
				if err != nil || !bytes.Equal(value, b.value) || b.lookup == nil || *b.lookup != request || b.get == nil || b.get.Authority != request.Authority || b.get.Digest != outputDigest(b.value) {
					t.Fatalf("value bytes=%d lookup=%+v get=%+v error=%v", len(value), b.lookup, b.get, err)
				}
			} else if err == nil || len(value) != 0 {
				t.Fatalf("refused read returned %d bytes, error=%v", len(value), err)
			}
			if b.reader != nil && !b.reader.closed {
				t.Fatal("output reader was not closed")
			}
			if (mode == "lookup" || mode == "close") && !errors.Is(err, outputRefused) {
				t.Fatalf("lost backend failure: %v", err)
			}
			if mode == "corrupt" && !errors.Is(err, blob.ErrIntegrity) || (mode == "missing_record" || mode == "missing_bytes") && !errors.Is(err, blob.ErrNotFound) || mode == "forbidden" && !errors.Is(err, blob.ErrDigestNotReadable) {
				t.Fatalf("lost required-read error: %v", err)
			}
		})
	}
}

func TestExecutorOutputCommit(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"success", "put", "commit", "result_refused"} {
		t.Run(mode, func(t *testing.T) {
			input, records, b, st := outputFixture(t, mode)
			input.Attempt--
			if mode == "result_refused" {
				st.reject = store.CauseReport
			}
			var trace []string
			process := newExecutionSDK(input.Accepted.Plan, input, "output_refused", &trace)
			executor, err := NewNodeExecutor(ExecutorDependencies{
				Store: st, Masker: secrets.NewMasker(), PublishOutput: records.PublishOutput,
				Bind: func(_ context.Context, in NodeInput) (ProcessGrant, error) {
					binding := in.RouteBinding
					binding.Routes.Bearer = "delegated-process"
					process.input.RouteBinding = binding
					return ProcessGrant{Binding: binding, Revoke: func() error { return nil }}, nil
				},
				Start: func(_ context.Context, in NodeInput, _ ExecutorActivity) (ExecutorProcess, error) {
					process.input = in
					return process, nil
				},
				EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
					return input.Engine, nil
				},
				ChildContext: func(ctx context.Context, _, _ time.Duration) (context.Context, context.CancelFunc, error) {
					child, cancel := context.WithCancel(ctx)
					return child, cancel, nil
				}, Validation: nodeproto.ValidationContext{
					Placement: nodeproto.PlacementLocal,
					Secrets:   noV2Secrets{},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			err = executor.Execute(t.Context(), input)
			if (err == nil) != (mode == "success") || !process.closed || !process.cleaned {
				t.Fatalf("closed=%v cleanup=%v error=%v", process.closed, process.cleaned, err)
			}
			if len(st.reports) != 2 || st.reports[0].Cause != store.CauseStart || st.reports[1].Cause != store.CauseReport {
				t.Fatalf("transitions=%+v", st.reports)
			}
			result := st.reports[1].Extra.Result
			if mode == "put" || mode == "commit" {
				if result == nil || result.Outcome != store.NodeFailed || result.OutputRecord != nil {
					t.Fatalf("refused output reported %+v", result)
				}
			} else if result == nil || result.Outcome != store.NodeSuccess || result.OutputRecord == nil || *result.OutputRecord != b.upload.Key || b.commit == nil {
				t.Fatalf("committed output reported %+v", result)
			}
		})
	}
}
