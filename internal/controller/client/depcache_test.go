package client

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func depcacheAuthority() blob.Authority {
	return blob.Authority{Repo: "gh:42", Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: "team", RunID: "run", NodeID: "build", Generation: 1, Attempt: 1}}, Ref: blob.RefFacts{Scope: blob.RefScope{Ref: "feature", Writer: "account/fixture"}, PullRequestBase: runspec.Optional[string]{Set: true, Value: "base"}, DefaultBranch: "main"}}
}

func TestDataClientReadsExecutionDepcacheWithoutExposingAuthority(t *testing.T) {
	authority := depcacheAuthority()
	for _, tc := range []struct {
		ref    string
		source blob.RestoreSource
	}{
		{"manual:account/fixture:feature", blob.RestoreOwnRef}, {"base", blob.RestorePRBase}, {"main", blob.RestoreDefaultBranch},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			_, records := testDataClient(t, func(request *http.Request) (*http.Response, error) {
				if request.URL.Path != "/prefix/api/v1/data/depcache/lookup" || request.Method != http.MethodPost {
					t.Fatalf("unexpected lookup: %s %s", request.Method, request.URL)
				}
				var fields map[string]any
				if err := json.NewDecoder(request.Body).Decode(&fields); err != nil {
					t.Fatal(err)
				}
				if len(fields) != 2 || fields["name"] != "modules" || fields["key"] != "dep-key" {
					t.Fatalf("cache transport exposed or changed selectors: %+v", fields)
				}
				return dataResponse(t, map[string]any{"found": true, "source": tc.source, "record": map[string]any{"pipeline": "", "job": "", "provenance": "", "created_at": time.Time{}, "kind": "depcache", "repo": "gh:42", "ref": tc.ref, "name": "modules", "key": "dep-key", "digest": strings.Repeat("a", 64)}}), nil
			})
			hit, err := records.LookupDepcache(t.Context(), blob.DepcacheLookup{Authority: authority, Key: blob.DepcacheKey{Repo: authority.Repo, Ref: authority.Ref.Scope, Name: "modules", Key: "dep-key"}})
			if err != nil || !hit.Found || hit.Record.Key.Team != "team" || hit.Source != tc.source || hit.Record.Digest != blob.Digest(strings.Repeat("a", 64)) || len(hit.ReadSet.Edges) != 0 {
				t.Fatalf("execution cache: %+v %v", hit, err)
			}
		})
	}
}

func FuzzDataClientDepcacheRejectsChangedScope(f *testing.F) {
	for field := uint8(0); field < 12; field++ {
		f.Add(field, "foreign")
	}
	f.Fuzz(func(t *testing.T, field uint8, value string) {
		if len(value) > 1024 {
			t.Skip()
		}
		authority := depcacheAuthority()
		record := map[string]any{"pipeline": "", "job": "", "provenance": "", "created_at": time.Time{}, "kind": "depcache", "repo": "gh:42", "ref": "manual:account/fixture:feature", "name": "modules", "key": "dep-key", "digest": strings.Repeat("a", 64)}
		source := blob.RestoreOwnRef
		switch field % 12 {
		case 0:
			record["kind"] = "output"
		case 1:
			record["repo"] = "other-" + value
		case 2:
			record["ref"] = "manual:other-" + value
		case 3:
			record["pipeline"] = "other-" + value
		case 4:
			record["job"] = "other-" + value
		case 5:
			record["key"] = "other-" + value
		case 6:
			record["name"] = "other-" + value
		case 7:
			record["digest"] = "invalid-" + value
		case 8:
			source = blob.RestoreDefaultBranch
		case 9:
			source = blob.RestorePRBase
		case 10:
			record["ref"] = "feature"
		case 11:
			source = blob.RestoreSource("unknown-" + value)
		}
		_, records := testDataClient(t, func(*http.Request) (*http.Response, error) {
			return dataResponse(t, map[string]any{"found": true, "source": source, "record": record}), nil
		})
		hit, err := records.LookupDepcache(t.Context(), blob.DepcacheLookup{Authority: authority, Key: blob.DepcacheKey{Repo: authority.Repo, Ref: authority.Ref.Scope, Name: "modules", Key: "dep-key"}})
		if err == nil || hit.Found {
			t.Fatalf("cache scope substitution accepted: %+v %v", hit, err)
		}
	})
}

func TestDataClientReadsLocalDepcacheWithoutGitRef(t *testing.T) {
	authority := depcacheAuthority()
	authority.Ref = blob.RefFacts{}
	authority.Repo = "local:checkout"
	_, records := testDataClient(t, func(*http.Request) (*http.Response, error) {
		return dataResponse(t, map[string]any{"found": true, "source": blob.RestoreOwnRef, "record": map[string]any{"pipeline": "", "job": "", "provenance": "", "created_at": time.Time{}, "kind": "depcache", "repo": "local:checkout", "ref": "manual:", "name": "modules", "key": "dep-key", "digest": strings.Repeat("a", 64)}}), nil
	})
	hit, err := records.LookupDepcache(t.Context(), blob.DepcacheLookup{Authority: authority, Key: blob.DepcacheKey{Repo: authority.Repo, Name: "modules", Key: "dep-key"}})
	if err != nil || !hit.Found || hit.Record.Key.Scope.Ref != (blob.RefScope{}) {
		t.Fatalf("cache without Git ref: %+v %v", hit, err)
	}
}
