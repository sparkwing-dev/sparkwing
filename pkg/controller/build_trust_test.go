package controller_test

import (
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestTeamOwnerCanChooseWhetherCloudTrustsLocalBinaries(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(523, "olga")
	var answer struct {
		TrustLocalBuilds bool `json:"trust_local_builds"`
	}
	if code := f.call("GET", "/api/v1/team/build-trust", olga.auth, nil, &answer); code != http.StatusOK || answer.TrustLocalBuilds {
		t.Fatalf("default trust = %d %+v", code, answer)
	}
	if code := f.call("PUT", "/api/v1/team/build-trust", olga.auth,
		map[string]bool{"trust_local_builds": true}, &answer); code != http.StatusOK || !answer.TrustLocalBuilds {
		t.Fatalf("enable trust = %d %+v", code, answer)
	}
	trust, err := teststore.ForTeam(t, f.store, store.Team(olga.team)).TrustLocalBuilds(t.Context())
	if err != nil || !trust {
		t.Fatalf("stored trust = %v, %v", trust, err)
	}
}
