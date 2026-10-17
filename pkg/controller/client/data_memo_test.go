package client

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	wire "github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestDataClientMemoUsesAuthenticatedSelection(t *testing.T) {
	authority := depcacheAuthority()
	key := blob.MemoKey{Repo: authority.Repo, Ref: authority.Ref.Scope, Pipeline: "ci", Job: "build", Hash: runspec.Hash(strings.Repeat("b", 64))}
	for _, copyHit := range []bool{false, true} {
		_, records := testDataClient(t, func(r *http.Request) (*http.Response, error) {
			operation := "lookup"
			if copyHit {
				operation = "copy"
			}
			if r.URL.Path != "/prefix/api/v1/data/memo/"+operation {
				t.Fatalf("memo route: %s", r.URL)
			}
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 3 || string(body["pipeline"]) != `"ci"` || string(body["job"]) != `"build"` || string(body["hash"]) != `"`+string(key.Hash)+`"` {
				t.Fatalf("memo selectors: %s", body)
			}
			digest := blob.Digest(strings.Repeat("a", 64))
			return dataResponse(t, wire.DataMemoLookupResult{Found: true, Output: &digest, Members: []wire.DataMember{}, Reader: &authority.Attempt.Value}), nil
		})
		lookup := blob.MemoLookup{Authority: authority, Key: key}
		var hit blob.MemoHit
		var err error
		if copyHit {
			hit, err = records.CopyMemo(t.Context(), blob.CopyMemoRequest{Lookup: lookup, Destination: authority.Attempt.Value})
		} else {
			hit, err = records.LookupMemo(t.Context(), lookup)
		}
		if err != nil || !hit.Found || !hit.Output.Set || hit.ReadSet.Reader != authority.Attempt.Value || len(hit.ReadSet.Edges) != 0 {
			t.Fatalf("memo result: %+v %v", hit, err)
		}
	}
}

func FuzzDataClientMemoCannotChangeReader(f *testing.F) {
	for field := uint8(0); field < 9; field++ {
		f.Add(field, "foreign", false)
		f.Add(field, "foreign", true)
	}
	f.Fuzz(func(t *testing.T, field uint8, value string, copyHit bool) {
		authority := depcacheAuthority()
		reader := authority.Attempt.Value
		digest := blob.Digest(strings.Repeat("a", 64))
		response := wire.DataMemoLookupResult{Found: true, Output: &digest, Members: []wire.DataMember{}, Reader: &reader}
		switch field % 9 {
		case 0:
			reader.Team += "other-" + value
		case 1:
			reader.RunID += "other-" + value
		case 2:
			reader.NodeID += "other-" + value
		case 3:
			reader.Generation++
		case 4:
			reader.Attempt++
		case 5:
			response.Reader = nil
		case 6:
			response.Found = false
		case 7:
			response.Output = nil
		case 8:
			digest = blob.Digest("invalid-" + value)
		}
		_, records := testDataClient(t, func(*http.Request) (*http.Response, error) { return dataResponse(t, response), nil })
		input := blob.MemoLookup{Authority: authority, Key: blob.MemoKey{Repo: authority.Repo, Ref: authority.Ref.Scope, Pipeline: "ci", Job: "build", Hash: "hash"}}
		var hit blob.MemoHit
		var err error
		if copyHit {
			hit, err = records.CopyMemo(t.Context(), blob.CopyMemoRequest{Lookup: input, Destination: authority.Attempt.Value})
		} else {
			hit, err = records.LookupMemo(t.Context(), input)
		}
		if err == nil || hit.Found {
			t.Fatalf("foreign reader or invalid result accepted: %+v %v", hit, err)
		}
	})
}
