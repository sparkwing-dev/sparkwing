package localcontroller

import (
	"encoding/hex"
	"reflect"
	"runtime"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

func FuzzClaimProfileIdentityIgnoresAmbientInput(f *testing.F) {
	f.Add("local:0", "forged-token")
	f.Add("S-1-5-18", "")
	f.Fuzz(func(t *testing.T, subject, bearer string) {
		t.Setenv("SPARKWING_RUNNER_NAME", hex.EncodeToString([]byte(subject)))
		t.Setenv("SPARKWING_TOKEN", hex.EncodeToString([]byte(bearer)))
		claimant, err := NewClaimProfile(nil)
		if err != nil {
			t.Fatal(err)
		}
		want, err := wingd.CurrentLocalSubject()
		if err != nil || claimant.subject != want {
			t.Fatalf("ambient input selected local identity: %q != %q: %v", claimant.subject, want, err)
		}
	})
}

func FuzzLocalClaimProfileRequiresCapturedAccount(f *testing.F) {
	f.Add("other", uint8(0))
	f.Add("local:0", uint8(1))
	f.Fuzz(func(t *testing.T, forged string, mutation uint8) {
		c := ClaimProfile{subject: "local:verified"}
		p := capability.Principal{
			Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "claimant"},
			Capability: capability.Capability{
				Prefix: "claimant", Team: string(store.DefaultTeam), Audience: capability.Controller,
				Subject: capability.Subject{Kind: capability.SubjectLocal, ID: c.subject},
			},
		}
		switch mutation % 6 {
		case 1:
			p.Capability.Subject.ID = "foreign:" + forged
		case 2:
			p.Capability.Team = "foreign:" + forged
		case 3:
			p.Capability.Audience = capability.NodeHost
		case 4:
			p.Authority.Kind = capability.AuthoritySession
		case 5:
			p.Authority.ID = "foreign:" + forged
		}
		profile, err := c.ResolveClaimProfile(t.Context(), p)
		if mutation%6 != 0 {
			if err == nil || profile.Name != "" {
				t.Fatal("foreign authority acquired local placement facts")
			}
			return
		}
		if err != nil || profile.Name != c.subject || profile.OS != runtime.GOOS || profile.Arch != runtime.GOARCH {
			t.Fatalf("verified account lost its machine facts: %+v: %v", profile, err)
		}
	})
}

func TestLocalClaimProfileAddsConfiguredLabelsWithoutAuthority(t *testing.T) {
	p := capability.Principal{
		Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "claimant"},
		Capability: capability.Capability{
			Prefix: "claimant", Team: string(store.DefaultTeam), Audience: capability.Controller,
			Subject: capability.Subject{Kind: capability.SubjectLocal, ID: "local:verified"},
		},
	}
	withLabels, err := NewClaimProfile([]string{"host=moonborn", "pool=prod", "name=other", "location=cloud", "local", "gpu"})
	if err != nil {
		t.Fatal(err)
	}

	p.Capability.Subject.ID = withLabels.subject
	bare, err := (&ClaimProfile{subject: withLabels.subject}).ResolveClaimProfile(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := withLabels.ResolveClaimProfile(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(profile.Labels, []string{"host=moonborn", "gpu"}) {
		t.Fatalf("labels = %v", profile.Labels)
	}
	profile.Labels = nil
	if !reflect.DeepEqual(profile, bare) {
		t.Fatalf("labels changed granted facts: %+v != %+v", profile, bare)
	}
}
