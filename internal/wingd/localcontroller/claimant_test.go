package localcontroller

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

func FuzzClaimantIdentityIgnoresAmbientInput(f *testing.F) {
	f.Add("local:0", "forged-token")
	f.Add("S-1-5-18", "")
	f.Fuzz(func(t *testing.T, subject, bearer string) {
		t.Setenv("SPARKWING_RUNNER_NAME", hex.EncodeToString([]byte(subject)))
		t.Setenv("SPARKWING_TOKEN", hex.EncodeToString([]byte(bearer)))
		s, err := teststore.OpenExisting(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		}()
		claimant, err := NewClaimant(s)
		if err != nil {
			t.Fatal(err)
		}
		want, err := wingd.CurrentLocalSubject()
		if err != nil || claimant.subject != want {
			t.Fatalf("ambient input selected local identity: %q != %q: %v", claimant.subject, want, err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if raw, err := claimant.Bearer(ctx); raw != "" || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled peer lookup issued a credential: %t: %v", raw != "", err)
		}
		if err := claimant.Close(); err != nil {
			t.Fatal(err)
		}
		if raw, err := claimant.Bearer(t.Context()); raw != "" || err == nil {
			t.Fatal("closed claimant returned authority")
		}
	})
}

func FuzzLocalClaimProfileRequiresCapturedAccount(f *testing.F) {
	f.Add("other", uint8(0))
	f.Add("local:0", uint8(1))
	f.Fuzz(func(t *testing.T, forged string, mutation uint8) {
		c := Claimant{subject: "local:verified"}
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
		profile, available, err := c.ResolveClaimProfile(t.Context(), p)
		if mutation%6 != 0 {
			if err == nil || profile.Name != "" || available.Cores != 0 {
				t.Fatal("foreign authority acquired local placement facts")
			}
			return
		}
		if err != nil || profile.Name != c.subject || profile.OS != runtime.GOOS || profile.Arch != runtime.GOARCH || profile.Capacity != available || available.Cores != float64(runtime.NumCPU()) {
			t.Fatalf("verified account lost its machine facts: %+v: %v", profile, err)
		}
	})
}
