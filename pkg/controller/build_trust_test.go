package controller_test

import (
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"net/http"
	"testing"
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
	trust, err := f.store.TrustLocalBuilds(t.Context(), store.Team(olga.team))
	if err != nil || !trust {
		t.Fatalf("stored trust = %v, %v", trust, err)
	}
}
