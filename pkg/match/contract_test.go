package match

import "testing"

func TestEvaluateReservationsAndGrantedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile Profile
		demand  Demand
		reason  Reason
	}{
		{"granted", Profile{ReservedPools: []string{"prod"}}, Demand{Selector: []string{"pool=prod"}, AllowedPools: []string{"prod"}}, ""},
		{"unrequired", Profile{ReservedPools: []string{"prod"}}, Demand{AllowedPools: []string{"prod"}}, ReasonReservation},
		{"not allowed", Profile{ReservedPools: []string{"prod"}}, Demand{Selector: []string{"pool=prod"}}, ReasonReservation},
		{"alternative cannot bypass", Profile{ReservedPools: []string{"prod"}, Labels: []string{"gpu"}}, Demand{Selector: []string{"pool=other,gpu"}, AllowedPools: []string{"prod"}}, ReasonReservation},
		{"asserted pool", Profile{Labels: []string{"pool=prod"}}, Demand{Selector: []string{"pool=prod"}, AllowedPools: []string{"prod"}}, ReasonSelector},
		{"asserted name", Profile{Name: "actual", Labels: []string{"name=other"}}, Demand{Selector: []string{"name=other"}}, ReasonSelector},
		{"asserted class", Profile{Class: ClassAgent, Labels: []string{"class=cloud"}}, Demand{Selector: []string{"class=cloud"}}, ReasonSelector},
		{"asserted location", Profile{Labels: []string{"location=cloud"}}, Demand{Selector: []string{"location=cloud"}}, ReasonTrustedPlacement},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Evaluate(tc.profile, tc.demand); got.Reason != tc.reason {
				t.Fatalf("verdict = %+v, want %s", got, tc.reason)
			}
		})
	}
}
