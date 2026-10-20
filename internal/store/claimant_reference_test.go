package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type claimantModel struct {
	tier                               string
	owned, automated, excluded, funded bool
}

func (m claimantModel) eligible() bool {
	return !m.excluded && (m.tier != "personal" || m.owned && !m.automated) && (m.tier != "cloud" || m.funded)
}

func TestClaimRankingAndNoticeMatchIndependentClaimantModel(t *testing.T) {
	for _, model := range []claimantModel{
		{tier: "personal", owned: true, funded: true},
		{tier: "personal", owned: false, funded: true},
		{tier: "personal", owned: true, automated: true, funded: true},
		{tier: "personal", owned: true, excluded: true, funded: true},
		{tier: "self-hosted", funded: true},
		{tier: "self-hosted", automated: true, funded: true},
		{tier: "self-hosted", excluded: true, funded: true},
		{tier: "github-actions", funded: true},
		{tier: "github-actions", excluded: true, funded: true},
		{tier: "cloud", funded: true},
		{tier: "cloud", excluded: true, funded: true},
		{tier: "cloud", funded: false},
	} {
		name := model.tier
		if model.owned {
			name += "/owned"
		}
		if model.automated {
			name += "/automated"
		}
		if model.excluded {
			name += "/excluded"
		}
		if !model.funded {
			name += "/unfunded"
		}
		t.Run(name, func(t *testing.T) {
			f := rankingFixture(t, model.funded)
			if _, err := f.Store.DB().ExecContext(t.Context(), `UPDATE triggers SET status='done'`); err != nil {
				t.Fatal(err)
			}
			prefix := f.Self
			switch model.tier {
			case "personal":
				prefix = f.Second
				if model.owned {
					prefix = f.Personal
				}
			case "github-actions":
				prefix = f.Actions
			case "cloud":
				prefix = f.Launcher
			}
			if model.automated {
				f.Request(t, func(r *runspec.Request) { r.Origin = runspec.OriginApp })
			}
			if model.excluded {
				ranking := []string{"personal", "github-actions", "self-hosted", "cloud"}
				filtered := ranking[:0]
				for _, tier := range ranking {
					if tier != model.tier {
						filtered = append(filtered, tier)
					}
				}
				if err := f.Rank(t, f.Admin, filtered...); err != nil {
					t.Fatal(err)
				}
			}
			f.At(8 * time.Second)
			f.Poll(t, prefix, capability.UnitNode)
			tenant, err := f.Store.ForTeam(t.Context(), "a")
			if err != nil {
				t.Fatal(err)
			}
			view, err := tenant.ReadRunView(t.Context(), "run")
			if err != nil || view.Waiting == nil {
				t.Fatalf("notice: %+v %v", view.Waiting, err)
			}
			standingEligible := view.Waiting.NoEligible == "" && view.Waiting.CreditHold == ""
			if standingEligible != model.eligible() {
				t.Fatalf("notice %+v; model eligible=%t", view.Waiting, model.eligible())
			}
			claim, err := f.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: prefix, Kinds: []capability.UnitKind{capability.UnitNode}, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}})
			if (err == nil) != model.eligible() {
				t.Fatalf("claim %+v %v; model eligible=%t", claim, err, model.eligible())
			}
			if err == nil {
				principal, err := f.Store.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
				if err != nil {
					t.Fatal(err)
				}
				owned, err := tenant.OwnerMachineClaimant(t.Context(), principal, "run")
				if err != nil || owned != (model.tier == "personal" && model.owned) {
					t.Fatalf("owner=%t err=%v", owned, err)
				}
			}
		})
	}
}

func TestRankingWindowMatchesIndependentPreferenceOrder(t *testing.T) {
	for _, order := range [][]string{{"personal", "self-hosted"}, {"self-hosted", "personal"}} {
		t.Run(order[0], func(t *testing.T) {
			f := rankingFixture(t, true)
			if err := f.Rank(t, f.Admin, order...); err != nil {
				t.Fatal(err)
			}
			f.Poll(t, f.Personal, capability.UnitNode)
			f.Poll(t, f.Self, capability.UnitNode)
			f.At(time.Second)
			first, second := f.Personal, f.Self
			if order[0] == "self-hosted" {
				first, second = second, first
			}
			wantNone(t, f, second, capability.UnitNode)
			wantAward(t, f, first, capability.UnitNode, "job")
		})
	}
}
