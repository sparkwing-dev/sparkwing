package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type selectedOutputRecords struct {
	blob.RecordStore
	t         *testing.T
	key       blob.RecordKey
	authority blob.Authority
	reads     int
}

func (r *selectedOutputRecords) LookupRecord(_ context.Context, request blob.RecordLookup) (blob.RecordLookupResult, error) {
	if request.Key != r.key || request.Authority != r.authority {
		r.t.Fatal("host changed the committed selector or reader authority")
	}
	r.reads++
	return blob.RecordLookupResult{Found: true, Record: blob.Record{Key: r.key, Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}, nil
}

func TestOwnerEvaluationStorageReadsMaterializedSelector(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "recorded", true: "missing"}[missing], func(t *testing.T) {
			input, state, deps, process := evaluationFixture(t)
			input.Generation, state.row.Generation, state.row.Attempt, state.row.Outcome = 0, 0, 0, store.NodeMemoized
			key := blob.MaterializedRecordKey("a", string(state.row.Ref.RunID), string(state.row.Ref.NodeID), 4, "gh:1", blob.RefScope{}, "ci", blob.KindOutput)
			state.row.OutputRecord = &key
			if missing {
				state.row.OutputRecord = nil
				var job nodeproto.JobSpec
				if err := json.Unmarshal(state.row.Spec, &job); err != nil {
					t.Fatal(err)
				}
				job.ResultStep = "result"
				state.row.Spec, _ = json.Marshal(job)
				hash, _ := executorHash(job)
				state.row.SpecHash, input.SpecHash = string(hash), string(hash)
			}
			records := &selectedOutputRecords{t: t, key: key, authority: blob.Authority{CapabilityRef: "owner"}}
			data := &scriptedStorage{onGet: func(context.Context, blob.GetRequest) (blob.VerifiedReader, error) {
				return &outputReader{Reader: bytes.NewReader([]byte(`{"count":2}`))}, nil
			}}
			input.Storage = StorageBinding{Bytes: data, Records: records, Authority: records.authority}
			deps.ReadOutput = nil
			process.answer = func(_ context.Context, request nodeproto.Request) (any, error) {
				if request.Op == nodeproto.OpEval {
					if string(request.Input) != `{"count":2}` {
						t.Fatalf("generator lost copied output: %s", request.Input)
					}
					return nodeproto.EvalResult{Value: json.RawMessage(`[]`)}, nil
				}
				return nil, nil
			}
			host, err := NewOwnerEvaluator(deps)
			if err != nil {
				t.Fatal(err)
			}
			body, failure, err := host.Expand(t.Context(), input)
			if err != nil || (failure != nil) != missing || !missing && (records.reads != 1 || string(body) != `{"jobs":[]}`) || missing && records.reads != 0 {
				t.Fatalf("body=%s failure=%+v reads=%d: %v", body, failure, records.reads, err)
			}
		})
	}
}

func TestOwnerEvaluationGivesAnUntypedSourceANullResult(t *testing.T) {
	input, state, deps, process := evaluationFixture(t)
	state.row.OutputRecord = nil
	input.Storage = StorageBinding{Bytes: &scriptedStorage{}, Records: &selectedOutputRecords{t: t}, Authority: blob.Authority{CapabilityRef: "owner"}}
	deps.ReadOutput = nil
	process.answer = func(_ context.Context, request nodeproto.Request) (any, error) {
		if request.Op == nodeproto.OpEval {
			if string(request.Input) != `null` {
				t.Fatalf("untyped source evaluated with %s", request.Input)
			}
			return nodeproto.EvalResult{Value: json.RawMessage(`[]`)}, nil
		}
		return nil, nil
	}
	host, err := NewOwnerEvaluator(deps)
	if err != nil {
		t.Fatal(err)
	}
	if body, failure, err := host.Expand(t.Context(), input); err != nil || failure != nil || string(body) != `{"jobs":[]}` {
		t.Fatalf("untyped source expansion: %s %+v %v", body, failure, err)
	}
}
