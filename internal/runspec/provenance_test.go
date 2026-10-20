package runspec_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

const provenanceSHA = "0123456789abcdef0123456789abcdef01234567"

func TestProvenanceRoundTripsAndRefusesForgedSeals(t *testing.T) {
	app := runspec.AppFacts{Event: "push", DeliveryKey: "d", RepositoryID: 7, Ref: "refs/heads/main", SHA: provenanceSHA, DefaultBranch: "main", DefaultBranchPush: true, VerifiedAt: time.Unix(1, 0)}
	verified, err := runspec.AppProvenance(app, runspec.Optional[runspec.PullRequest]{}, "deploy")
	if err != nil || verified.Trust() != runspec.TrustVerifiedDefault {
		t.Fatalf("verified = %+v, %v", verified, err)
	}
	raw, err := json.Marshal(verified)
	if err != nil {
		t.Fatal(err)
	}
	var back runspec.Provenance
	if err := json.Unmarshal(raw, &back); err != nil || back != verified {
		t.Fatalf("round trip = %+v, %v", back, err)
	}
	for _, forged := range []string{
		`{"trust":"verified","ref":"refs/heads/main","sha":"` + provenanceSHA + `","pipeline":"deploy","event":"push","delivery":"d"}`,
		`{"trust":"asserted","pipeline":"deploy","writer":"account/a","repository_id":7}`,
		`{"trust":"asserted","pipeline":"deploy"}`,
		`{"trust":"asserted","pipeline":"deploy","writer":"a:b"}`,
		`{"trust":"root","pipeline":"deploy","writer":"a"}`,
		`{"trust":"actions","pipeline":"deploy","writer":"actions","ref":"refs/heads/main","sha":"short"}`,
	} {
		if err := json.Unmarshal([]byte(forged), &back); err == nil {
			t.Fatalf("decoded a seal no constructor produces: %s", forged)
		}
	}
	if (runspec.Provenance{}).Valid() {
		t.Fatal("the zero provenance speaks for a run")
	}
	asserted, err := runspec.AssertedProvenance(runspec.PrincipalWriter("account", "a:b/c"), "refs/heads/main", provenanceSHA, "deploy")
	if err != nil || strings.Contains(string(asserted.Writer()), ":") {
		t.Fatalf("writer escapes a separator: %q, %v", asserted.Writer(), err)
	}
	if trigger, id, ref, sha := asserted.OIDC(); trigger != "manual" || id != 0 || ref != "" || sha != "" {
		t.Fatalf("asserted run claims %s %d %q %q", trigger, id, ref, sha)
	}
	app.DefaultBranchPush, app.Ref = false, "refs/heads/feature"
	feature, err := runspec.AppProvenance(app, runspec.Optional[runspec.PullRequest]{}, "deploy")
	if err != nil || feature.Trust() != runspec.TrustVerified {
		t.Fatalf("feature = %+v, %v", feature, err)
	}
}

// 11 S1: one selector serves App subscriptions and pool rules, so a rule written
// as "main" matches the refs/heads/main a delivery seals, and globs match.
func TestBranchSelectorNormalizesAndGlobs(t *testing.T) {
	selector, err := runspec.ParseBranchSelector([]string{"refs/heads/main", "release/*"})
	if err != nil {
		t.Fatal(err)
	}
	for branch, want := range map[string]bool{"main": true, "refs/heads/main": true, "release/1.2": true, "refs/heads/release/1.2": true, "release/1/2": false, "feature": false} {
		if selector.Matches(branch) != want {
			t.Fatalf("%q matched %t", branch, !want)
		}
	}
	if _, err := runspec.ParseBranchSelector([]string{"["}); err == nil {
		t.Fatal("malformed glob accepted")
	}
	if !(runspec.BranchSelector{}).Matches("anything") {
		t.Fatal("an empty selector filters branches")
	}
}
