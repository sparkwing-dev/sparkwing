package controller_test

import (
	"encoding/json"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestTeamOwnerCanChooseWhetherCloudTrustsLocalBinaries(t *testing.T) {
	f := newBrowserFixture(t, nil)
	read := func(want bool) {
		t.Helper()
		rec := f.serve(f.cookieRequest("GET", "/api/v1/team/build-trust", ""))
		var answer struct {
			TrustLocalBuilds bool `json:"trust_local_builds"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil || rec.Code != 200 || answer.TrustLocalBuilds != want {
			t.Fatalf("trust = %d %s: %v", rec.Code, rec.Body, err)
		}
		got, err := teststore.ForTeam(t, f.st, store.DefaultTeam).TrustLocalBuilds(t.Context())
		if err != nil || got != want {
			t.Fatalf("stored trust = %v: %v", got, err)
		}
	}
	read(false)
	for _, body := range []string{`{"trust_local_builds":true}`, `{"trust_local_builds":false}`} {
		if rec := f.serve(f.cookieRequest("PUT", "/api/v1/team/build-trust", body)); rec.Code != 200 {
			t.Fatalf("write = %d %s", rec.Code, rec.Body)
		}
		read(body == `{"trust_local_builds":true}`)
	}
}

func TestBuildTrustMutationStaysWithItsTeamAndCurrentOwnerProperty(t *testing.T) {
	f := newBrowserFixture(t, nil)
	teststore.SeedTeam(t, f.st, "foreign")
	property := func(role uint8, enabled bool) bool {
		current := []string{"owner", "editor", "reader"}[role%3]
		if _, err := teststore.SQL(t, f.st).Exec(`UPDATE memberships SET role=? WHERE team='default' AND account_id='alice'`, current); err != nil {
			t.Fatal(err)
		}
		before, err := teststore.ForTeam(t, f.st, store.DefaultTeam).TrustLocalBuilds(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(map[string]bool{"trust_local_builds": enabled})
		if err != nil {
			t.Fatal(err)
		}
		rec := f.serve(f.cookieRequest("PUT", "/api/v1/team/build-trust", string(body)))
		wantStatus, want := 403, before
		if current == "owner" {
			wantStatus, want = 200, enabled
		}
		got, err := teststore.ForTeam(t, f.st, store.DefaultTeam).TrustLocalBuilds(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		foreign, err := teststore.ForTeam(t, f.st, "foreign").TrustLocalBuilds(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return rec.Code == wantStatus && got == want && !foreign
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48}); err != nil {
		t.Fatal(err)
	}
}
