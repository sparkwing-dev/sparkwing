package capability

import (
	"reflect"
	"testing"
)

// Reading policy must not change authorization: writing through any accessor's
// result leaves the next read unchanged.
func TestPolicyAccessorsShareNoSlices(t *testing.T) {
	before := Routes()
	for _, route := range Routes() {
		got, ok := LookupRoute(route.Method, route.Path)
		if !ok {
			t.Fatalf("%s %s missing", route.Method, route.Path)
		}
		for i := range got.Access {
			got.Access[i] = AccessRule{Grants: []Grant{RunsRead}}
		}
		for _, access := range route.Access {
			for j := range access.Grants {
				access.Grants[j] = RunsRead
			}
		}
	}
	if !reflect.DeepEqual(before, Routes()) {
		t.Fatal("writing through Routes or LookupRoute changed the route table")
	}
	awards := AwardPolicies()
	for _, policy := range AwardPolicies() {
		for i := range policy.Grants {
			policy.Grants[i] = RunsRead
		}
		for i := range policy.ClaimGrants {
			policy.ClaimGrants[i] = RunsRead
		}
	}
	if !reflect.DeepEqual(awards, AwardPolicies()) {
		t.Fatal("writing through AwardPolicies changed the award table")
	}
}
