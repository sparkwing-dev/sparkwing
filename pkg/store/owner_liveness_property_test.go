package store

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestOwnerLeaseAloneDeterminesLivenessProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(53, 101))
	for i := range 24 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, demand, owner, now := renewalFixture(t, capability.UnitTrigger)
			tenant := &Tenant{s: s, team: owner.Team}
			root := authorityRoot("remote", "a")
			root.Grants = []capability.Grant{capability.NodesClaim}
			authorityPersist(t, s, root)
			demand.CapabilityPrefix = root.Prefix
			demand.Target = &UnitRef{Kind: capability.UnitNode, RunID: owner.RunID, NodeID: "job"}
			remote, err := s.ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			ownerEnd := owner.Award.LeaseExpiresAt
			for range random.IntN(4) + 1 {
				*now = now.Add(time.Duration(random.IntN(5)+1) * time.Second)
				beat, err := s.RenewUnit(t.Context(), owner.Award.CapabilityPrefix, false)
				if err != nil || !beat.ExpiresAt.After(ownerEnd) {
					t.Fatalf("owner renewal: %+v, %v", beat, err)
				}
				ownerEnd = beat.ExpiresAt
				if _, err := s.RenewUnit(t.Context(), remote.Award.CapabilityPrefix, false); err != nil {
					t.Fatal(err)
				}
				if reaped, err := s.ReapUnits(t.Context(), lease, 10); err != nil || len(reaped) != 0 {
					t.Fatalf("renewed owner recovered: %+v, %v", reaped, err)
				}
			}
			*now = ownerEnd.Add(-time.Second)
			remoteBeat, err := s.RenewUnit(t.Context(), remote.Award.CapabilityPrefix, false)
			if err != nil || !remoteBeat.ExpiresAt.After(ownerEnd) {
				t.Fatalf("remote renewal: %+v, %v", remoteBeat, err)
			}
			*now = ownerEnd
			before, err := tenant.ReadSnapshot(t.Context(), owner.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.RenewUnit(t.Context(), owner.Award.CapabilityPrefix, false); err == nil {
				t.Fatal("expired owner renewed itself")
			}
			after, err := tenant.ReadSnapshot(t.Context(), owner.RunID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("refused renewal changed work: %v", err)
			}
			reaped, err := s.ReapUnits(t.Context(), lease, 10)
			if err != nil || len(reaped) != 1 || !reaped[0].Requeued || reaped[0].RunID != owner.RunID || reaped[0].Disruptions != 0 {
				t.Fatalf("remote activity retained lost owner: %+v, %v", reaped, err)
			}
			job, err := tenant.ReadNode(t.Context(), NodeRef{owner.RunID, remote.NodeID})
			if err != nil || job.CapabilityPrefix != remote.Award.CapabilityPrefix || job.LeaseExpiresAt == nil || !job.LeaseExpiresAt.Equal(remoteBeat.ExpiresAt) {
				t.Fatalf("owner recovery changed remote claim: %+v, %v", job, err)
			}
		})
	}
}
