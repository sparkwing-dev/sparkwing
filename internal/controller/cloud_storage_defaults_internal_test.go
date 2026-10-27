package controller

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func cloudDefaultsServer(t *testing.T, cfg deployment.Config) (*Server, *store.Store) {
	t.Helper()
	s, err := teststore.OpenExisting(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return NewDeployment(s, cfg, nil), s
}

// A hosted deployment's composed store puts an unfunded team on the free storage tier, while a self-hosted one
// applies no automatic tier; the sign-up gate reads no free-tier state on either.
func TestHostedDeploymentAppliesCloudStorageDefaults(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  deployment.Config
		want store.StorageQuota
	}{
		{"cloud", deploymenttest.Cloud(t, "https://cloud.example.com"), func() store.StorageQuota {
			q, ok := store.TierQuota("free")
			if !ok {
				t.Fatal("no free tier")
			}
			q.Team = "a"
			return q
		}()},
		{"self-hosted team", deploymenttest.Team(t), store.StorageQuota{Team: "a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, s := cloudDefaultsServer(t, tc.cfg)
			teststore.SeedTeam(t, s, "a")
			tenant, err := srv.resolveTenant(t.Context(), "a")
			if err != nil {
				t.Fatal(err)
			}
			got, err := tenant.(*store.Tenant).StorageQuota(t.Context())
			if err != nil || got != tc.want {
				t.Fatalf("quota = %+v, %v; want %+v", got, err, tc.want)
			}

			if c := srv.signUpConditions(t.Context()); c.ForceWaitlist {
				t.Fatal("sign-up conditions force the waitlist; want none")
			}
			status, err := srv.signUpStatus(t.Context(), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(status)
			if err != nil {
				t.Fatal(err)
			}
			if status.State != string(store.SignUpOpen) || len(status.Reasons) != 0 || strings.Contains(string(raw), "free_tier") {
				t.Fatalf("sign-up status = %s; want open with no free-tier field or reason", raw)
			}
		})
	}
}
