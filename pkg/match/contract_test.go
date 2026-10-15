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
		{"capacity", Profile{Capacity: Resources{Cores: 2}}, Demand{Request: Resources{Cores: 4}}, ReasonShape},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Evaluate(tc.profile, tc.demand); got.Reason != tc.reason {
				t.Fatalf("verdict = %+v, want %s", got, tc.reason)
			}
		})
	}
}

func TestEvaluatePreferencesCannotRelaxRequirements(t *testing.T) {
	p := Profile{Labels: []string{"gpu"}}
	d := Demand{Selector: []string{"arm64"}, Prefers: []string{"gpu"}}
	if got := Evaluate(p, d); got.OK() || got.PreferenceScore != 0 {
		t.Fatalf("ineligible preference = %+v", got)
	}
	d.Selector = []string{"gpu"}
	d.Prefers = []string{"gpu", "arm64"}
	if got := Evaluate(p, d); !got.OK() || got.PreferenceScore != 1 {
		t.Fatalf("preference = %+v", got)
	}
}
