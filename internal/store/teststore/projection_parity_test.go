package teststore_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestSeedRunProjectsPersistedProvenance(t *testing.T) {
	for _, ref := range []string{"main", "feature-\xff\xfe"} {
		t.Run(ref, func(t *testing.T) {
			s, err := teststore.Open(t, filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			seed := store.Run{RunSummary: runstate.RunSummary{
				ID: "report", Pipeline: "build", Status: "success", DeclaredRepo: "acme/widgets",
				GitBranch: ref, GitSHA: strings.Repeat("A", 40),
			}}
			if err := teststore.SeedRun(s, t.Context(), store.DefaultTeam, seed); err != nil {
				t.Fatal(err)
			}
			var body []byte
			var sha, projectedRef string
			if err := teststore.SQL(t, s).QueryRowContext(t.Context(), `SELECT request_json,provenance_sha,provenance_ref FROM runs WHERE id=?`, seed.ID).Scan(&body, &sha, &projectedRef); err != nil {
				t.Fatal(err)
			}
			var sealed runspec.SealedRequest
			if err := json.Unmarshal(body, &sealed); err != nil {
				t.Fatal(err)
			}
			if sha != sealed.Provenance.SHA() || projectedRef != sealed.Provenance.Ref() {
				t.Fatalf("projection differs from persisted seal: %q/%q, want %q/%q", sha, projectedRef, sealed.Provenance.SHA(), sealed.Provenance.Ref())
			}
		})
	}
}

func TestSeedRunLeavesCallerArgumentsUntouched(t *testing.T) {
	s, err := teststore.Open(t, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	seed := store.Run{RunSummary: runstate.RunSummary{
		ID: "report", Pipeline: "build", Status: "success", DeclaredRepo: "acme/widgets",
		Args: map[string]string{"value": "bad\xff", "key\xff": "original"},
	}}
	if err := teststore.SeedRun(s, t.Context(), store.DefaultTeam, seed); err != nil {
		t.Fatal(err)
	}
	if len(seed.Args) != 2 || seed.Args["value"] != "bad\xff" || seed.Args["key\xff"] != "original" {
		t.Fatalf("seeding mutated caller arguments: %#v", seed.Args)
	}
}
