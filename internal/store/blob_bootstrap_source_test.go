package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestBootstrapSourceBundleReadStaysBoundBeforeExchange(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitNode, capability.UnitPlan, capability.UnitEval} {
		for attack := range 10 {
			t.Run(fmt.Sprintf("%s/%d", kind, attack), func(t *testing.T) {
				checkBootstrapSourceBundle(t, kind, uint8(attack), uint16(1), "bootstrap-source")
			})
		}
	}
}

func FuzzBootstrapSourceBundleReadStaysBound(f *testing.F) {
	for attack := range 10 {
		for kind := range 3 {
			f.Add(uint8(kind), uint8(attack), uint16(1), "source bundle")
		}
	}
	f.Fuzz(func(t *testing.T, kind, attack uint8, offset uint16, seed string) {
		kinds := []capability.UnitKind{capability.UnitNode, capability.UnitPlan, capability.UnitEval}
		checkBootstrapSourceBundle(t, kinds[int(kind)%len(kinds)], attack%10, offset, seed)
	})
}

func checkBootstrapSourceBundle(t *testing.T, kind capability.UnitKind, attack uint8, offset uint16, seed string) {
	t.Helper()
	s, _, claim, now := launcherSnapshotFixture(t, kind, int64(offset))
	digest := sha256.Sum256([]byte(seed))
	sourceDigest := hex.EncodeToString(digest[:])
	seedSourceSetupBundle(t, s, sourceDigest)
	tenant, err := s.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	authority := blob.Authority{CapabilityRef: claim.Award.CapabilityPrefix}
	p, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: authority.CapabilityRef})
	if err != nil || !slices.Equal(p.Capability.Grants, []capability.Grant{capability.SourceRead, capability.ClaimExchange}) {
		t.Fatalf("bootstrap authority widened: %+v %v", p.Capability.Grants, err)
	}
	if kind == capability.UnitNode && (p.Capability.Binding.Attempt != 0 || p.Capability.Data.Attempt != 0) {
		t.Fatal("fresh node award bound a historical execution attempt")
	}
	for _, grant := range []capability.Grant{capability.DataRead, capability.DataWrite} {
		if _, err := tenant.ResolveBlobAuthority(t.Context(), authority, grant); err == nil {
			t.Fatalf("bootstrap acquired %s", grant)
		}
	}
	table := "nodes"
	if kind == capability.UnitPlan {
		table = "triggers"
	} else if kind == capability.UnitEval {
		table = "eval_units"
	}
	switch attack {
	case 1:
		rewriteSchemaExec(t, s.db, `UPDATE blob_records SET run_id=NULL WHERE kind='source_bundle'`)
	case 2:
		rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=? WHERE id='run'`, []byte(`{"request":{"git":{"repository":{"key":"gh:1"},"ref":"main"},"source":{"bundle_digest":{"set":true,"value":"foreign"}}},"provenance":{"trust":"asserted","ref":"main","pipeline":"ci","writer":"account/fixture"}}`))
	case 3:
		rewriteSchemaExec(t, s.db, `UPDATE blob_records SET kind='output' WHERE kind='source_bundle'`)
	case 4:
		rewriteSchemaExec(t, s.db, `UPDATE `+table+` SET generation=generation+?`, int64(offset)+1)
	case 5:
		rewriteSchemaExec(t, s.db, `UPDATE `+table+` SET claimed_by=?`, "attacker-"+sourceDigest)
	case 6:
		*now = claim.Award.LeaseExpiresAt.Add(time.Duration(offset))
	case 7:
		if _, err := s.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: authority.CapabilityRef}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: authority.CapabilityRef}); err == nil {
			t.Fatal("bootstrap exchanged twice")
		}
	case 8:
		rewriteSchemaExec(t, s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix='launcher'`)
	case 9:
		rewriteSchemaExec(t, s.db, `UPDATE runs SET cancel_requested_at=1 WHERE id='run'`)
	}
	read, err := tenant.ReadableBlobs(t.Context(), blob.HasManyRequest{Authority: authority, Digests: []blob.Digest{blob.Digest(sourceDigest)}})
	allowed := attack == 0
	if allowed && (err != nil || len(read.Present) != 1 || string(read.Present[0]) != sourceDigest) || !allowed && err == nil && len(read.Present) != 0 {
		t.Fatalf("bootstrap source read=%+v %v, allowed=%v", read, err, allowed)
	}
	route, ok := capability.LookupRoute("POST", "/api/v1/data/download")
	if !ok {
		t.Fatal("source download route missing")
	}
	authenticated, accessErr := capability.NewAuthenticator(s).Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialBearer, Raw: string(claim.Bearer)}, route)
	if accessErr == nil {
		scope := authenticated.Principal.Capability.Data
		target := capability.Target{Team: "a", Data: &capability.DataTarget{Operation: capability.DataDownload, Digest: sourceDigest, RunID: scope.RunID, NodeID: scope.NodeID, Generation: scope.Generation, Attempt: scope.Attempt}}
		var authorized capability.Authorized
		authorized, accessErr = capability.Authorize(t.Context(), s, route, authenticated, target)
		if accessErr == nil {
			accessErr = capability.AuthorizeData(t.Context(), tenant, authorized, *target.Data)
		}
		if accessErr == nil {
			projected, err := tenant.ResolveDataAuthority(t.Context(), authorized)
			if err != nil || projected.Attempt.Set || projected.Upload.Set {
				t.Fatalf("bootstrap source projection acquired execution authority: %+v %v", projected, err)
			}
		}
	}
	if (accessErr == nil) != allowed {
		t.Fatalf("bootstrap download authorization=%v, allowed=%v", accessErr, allowed)
	}
	if allowed {
		a := dataAccessAuthority(t, s, authority.CapabilityRef)
		for _, operation := range []capability.DataOperation{capability.DataUpload, capability.DataRecord, capability.DataHasMany, capability.DataLookup} {
			if err := tenant.CheckDataAccess(t.Context(), a, capability.DataTarget{Operation: operation, Digest: sourceDigest}); err == nil {
				t.Fatalf("bootstrap acquired %s authority", operation)
			}
		}
		exchanged, err := s.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: authority.CapabilityRef})
		if err != nil {
			t.Fatal(err)
		}
		if kind == capability.UnitNode {
			previousAttempt := int64(offset)
			started, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, To: NodeRunning, Cause: CauseStart, SourceAttempt: &previousAttempt, Actor: Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: exchanged.Capability.Prefix}, CapabilityPrefix: exchanged.Capability.Prefix, Holder: exchanged.Capability.Binding.Holder, Generation: exchanged.Capability.Binding.Generation}})
			if err != nil || started.Attempt != previousAttempt+1 {
				t.Fatalf("retry execution start: %+v %v", started, err)
			}
			active, err := tenant.ResolveBlobAuthority(t.Context(), blob.Authority{CapabilityRef: exchanged.Capability.Prefix}, capability.DataWrite)
			if err != nil || !active.Attempt.Set || active.Attempt.Value.Attempt != previousAttempt+1 || !active.Upload.Set {
				t.Fatalf("started execution lacks normal upload identity: %+v %v", active, err)
			}
		}
		if metadata, err := tenant.ReadDataBlob(t.Context(), blob.Authority{CapabilityRef: exchanged.Capability.Prefix}, blob.Digest(sourceDigest)); err != nil || metadata.Size != 3 {
			t.Fatalf("exchanged award lost sealed source: %+v %v", metadata, err)
		}
		if _, err := tenant.ResolveBlobAuthority(t.Context(), authority, capability.SourceRead); err == nil {
			t.Fatal("consumed bootstrap retained source authority")
		}
	}
}
