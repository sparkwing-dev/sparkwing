package main

import (
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerhost"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

func TestOnlySelfHostedProfilesComposeAHostClaimant(t *testing.T) {
	for _, profile := range []string{controller.LimitsProfileCloud, controller.LimitsProfileCloudFree, ""} {
		t.Run("profile="+profile, func(t *testing.T) {
			root := t.TempDir()
			st, err := openControllerStore(t.Context(), filepath.Join(root, "state.db"), "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			srv, err := newControllerServer(st, root, "development", nil)
			if err != nil {
				t.Fatal(err)
			}
			live := func() int {
				var count int
				if err := st.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM capabilities WHERE subject_id='controller-host' AND revoked_at IS NULL`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				return count
			}
			host, err := controllerhost.New(t.Context(), srv, hostConfig(profile, root))
			if err != nil {
				t.Fatal(err)
			}
			cloud := profile == controller.LimitsProfileCloud || profile == controller.LimitsProfileCloudFree
			if cloud {
				if host != nil || live() != 0 {
					t.Fatalf("a Cloud deployment composed a host claimant: %v, %d live roots", host, live())
				}
				return
			}
			if host == nil || live() != 1 {
				t.Fatalf("a self-hosted deployment must compose one host claimant: %v, %d live roots", host, live())
			}
			if err := host.Close(t.Context()); err != nil || live() != 0 {
				t.Fatalf("closing the host must revoke its root: %v, %d live roots", err, live())
			}
		})
	}
}
