package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runevents"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func sealedSubscriptionFixture(t *testing.T, team Team, id, url, secret string) SealedRunEventSubscription {
	t.Helper()
	sealed, err := SealRunEventSubscription(fixtureSecretCipher(t), team, RunEventSubscription{
		ID: id, URL: url, Secret: secret,
		Filter: runevents.Filter{EventTypes: []string{runevents.EventFinished}, Statuses: []string{"failed"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func adminOf(t *testing.T, s *Store, id string, team Team) capability.Authorized {
	t.Helper()
	owner := authorityMember(t, s, id, RoleOwner, team)
	return capability.Authorized{Principal: capability.Principal{Authority: owner.Authority}, Target: capability.Target{Team: string(team)}, Access: capability.AccessRule{Credential: capability.CredentialMember, Grants: []capability.Grant{capability.TeamAdmin}, Binding: capability.BindNone, Liveness: capability.LiveCredential}}
}

func TestRunEventSubscriptionLifecycle(t *testing.T) {
	tenant, a := credentialMaintenanceFixture(t)
	id := RunEventSubscriptionID(tenant.team, "key-1")
	created, replayed, err := tenant.CreateRunEventSubscription(t.Context(), a, sealedSubscriptionFixture(t, tenant.team, id, "https://hooks.example/a", "whsec_one"))
	if err != nil || replayed || created.ID != id || created.CreatedPrincipal != "owner" || created.CreatedAt.IsZero() || created.Secret == "whsec_one" {
		t.Fatalf("create = %+v, %v, %v", created, replayed, err)
	}
	if created.Filter.Pipelines == nil || created.Filter.Repositories == nil {
		t.Fatalf("empty filters must read as empty lists: %+v", created.Filter)
	}
	again, replayed, err := tenant.CreateRunEventSubscription(t.Context(), a, sealedSubscriptionFixture(t, tenant.team, id, "https://hooks.example/a", "whsec_other"))
	if err != nil || !replayed || again.Secret != created.Secret {
		t.Fatalf("replay = %+v, %v, %v", again, replayed, err)
	}
	if _, _, err := tenant.CreateRunEventSubscription(t.Context(), a, sealedSubscriptionFixture(t, tenant.team, id, "https://hooks.example/b", "whsec_one")); !errors.Is(err, ErrRunEventSubscriptionConflict) {
		t.Fatalf("reused key with another URL: %v", err)
	}
	listed, err := tenant.RunEventSubscriptions(t.Context())
	if err != nil || len(listed) != 1 || listed[0].URL != "https://hooks.example/a" || listed[0].Filter.Statuses[0] != "failed" {
		t.Fatalf("list = %+v, %v", listed, err)
	}
	rotated, err := tenant.RotateRunEventSubscriptionSecret(t.Context(), a, sealedSubscriptionFixture(t, tenant.team, id, "", "whsec_two"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := fixtureSecretCipher(t).OpenBound(secrets.PurposeEventSigning, string(tenant.team), RunEventSecretName(id), "", false, true, rotated.Secret)
	if err != nil || plain != "whsec_two" {
		t.Fatalf("rotated secret opens to %q, %v", plain, err)
	}
	if deleted, err := tenant.DeleteRunEventSubscription(t.Context(), a, id); err != nil || !deleted {
		t.Fatalf("delete = %v, %v", deleted, err)
	}
	if deleted, err := tenant.DeleteRunEventSubscription(t.Context(), a, id); err != nil || deleted {
		t.Fatalf("second delete = %v, %v", deleted, err)
	}
	if _, err := tenant.RunEventSubscription(t.Context(), id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
	if _, err := tenant.RotateRunEventSubscriptionSecret(t.Context(), a, sealedSubscriptionFixture(t, tenant.team, id, "", "whsec_three")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rotate after delete: %v", err)
	}
	var audits int
	if err := tenant.s.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE team='a' AND kind='team.mutated' AND subject_id=?`, id).Scan(&audits); err != nil || audits != 5 {
		t.Fatalf("audit rows = %d, %v; want create, replay, rotate and two deletes", audits, err)
	}
}

func TestRunEventSubscriptionsStayInTheirTeam(t *testing.T) {
	tenant, a := credentialMaintenanceFixture(t)
	other := &Tenant{s: tenant.s, team: "b"}
	b := adminOf(t, tenant.s, "owner-b", "b")
	id := "sub_000000000000000000000001"
	if _, _, err := tenant.CreateRunEventSubscription(t.Context(), a, sealedSubscriptionFixture(t, "a", id, "https://hooks.example/a", "whsec_a")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := other.CreateRunEventSubscription(t.Context(), b, sealedSubscriptionFixture(t, "b", id, "https://hooks.example/b", "whsec_b")); err != nil {
		t.Fatalf("colliding ID in another team: %v", err)
	}
	if _, _, err := other.CreateRunEventSubscription(t.Context(), b, sealedSubscriptionFixture(t, "a", "sub_000000000000000000000002", "https://hooks.example/b", "whsec_b")); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a row sealed for team a stored in team b: %v", err)
	}
	if _, _, err := other.CreateRunEventSubscription(t.Context(), a, sealedSubscriptionFixture(t, "b", "sub_000000000000000000000003", "https://hooks.example/b", "whsec_b")); err == nil {
		t.Fatal("team a's administrator wrote team b's subscription")
	}
	rows, err := other.RunEventSubscriptions(t.Context())
	if err != nil || len(rows) != 1 || rows[0].URL != "https://hooks.example/b" {
		t.Fatalf("team b sees %+v, %v", rows, err)
	}
	mine, err := tenant.RunEventSubscription(t.Context(), id)
	if err != nil || mine.URL != "https://hooks.example/a" {
		t.Fatalf("team a reads %+v, %v", mine, err)
	}
	if _, err := fixtureSecretCipher(t).OpenBound(secrets.PurposeEventSigning, "b", RunEventSecretName(id), "", false, true, mine.Secret); err == nil {
		t.Fatal("team a's sealed secret opened as team b's")
	}
	if deleted, err := other.DeleteRunEventSubscription(t.Context(), b, "sub_000000000000000000000009"); err != nil || deleted {
		t.Fatalf("delete of an unknown ID = %v, %v", deleted, err)
	}
	if _, err := tenant.RunEventSubscription(t.Context(), id); err != nil {
		t.Fatalf("team b's delete reached team a: %v", err)
	}
}

func TestRunEventSubscriptionCapAndInputChecks(t *testing.T) {
	tenant, a := credentialMaintenanceFixture(t)
	for i := range MaxRunEventSubscriptions {
		id := RunEventSubscriptionID(tenant.team, fmt.Sprint(i))
		if _, _, err := tenant.CreateRunEventSubscription(t.Context(), a, sealedSubscriptionFixture(t, tenant.team, id, "https://hooks.example/a", "whsec_x")); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := tenant.CreateRunEventSubscription(t.Context(), a, sealedSubscriptionFixture(t, tenant.team, RunEventSubscriptionID(tenant.team, "over"), "https://hooks.example/a", "whsec_x")); !errors.Is(err, ErrRunEventSubscriptionLimit) {
		t.Fatalf("subscription past the cap: %v", err)
	}
	plain := SealedRunEventSubscription{team: tenant.team, row: RunEventSubscription{ID: RunEventSubscriptionID(tenant.team, "plain"), URL: "https://hooks.example/a", Secret: "whsec_plain", Filter: runevents.Filter{EventTypes: []string{runevents.EventFinished}}}}
	if _, _, err := tenant.CreateRunEventSubscription(t.Context(), a, plain); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("plaintext secret stored: %v", err)
	}
	bad := sealedSubscriptionFixture(t, tenant.team, "sub_UPPER", "https://hooks.example/a", "whsec_x")
	if _, _, err := tenant.CreateRunEventSubscription(t.Context(), a, bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("malformed ID stored: %v", err)
	}
	if !strings.HasPrefix(RunEventSubscriptionID("a", "k"), "sub_") || RunEventSubscriptionID("a", "k") == RunEventSubscriptionID("b", "k") {
		t.Fatal("subscription IDs must differ across teams for the same key")
	}
}

func TestRunEventSourceIsStableAcrossCalls(t *testing.T) {
	tenant, _ := credentialMaintenanceFixture(t)
	read := func(team Team) string {
		source, err := inTxResult(t.Context(), tenant.s, func(tx *storeTx) (string, error) { return runEventSourceTx(t.Context(), tx, team) })
		if err != nil {
			t.Fatal(err)
		}
		return source
	}
	first, second, other := read("a"), read("a"), read("b")
	if first != second || !strings.HasPrefix(first, "urn:sparkwing:") || !strings.HasSuffix(first, ":team:a") || strings.TrimSuffix(other, ":team:b") != strings.TrimSuffix(first, ":team:a") {
		t.Fatalf("sources %q %q %q", first, second, other)
	}
}

func TestRunEventDeliveryKeepsItsBodyAndFollowsItsRun(t *testing.T) {
	tenant, a := credentialMaintenanceFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,finished_at,log_retention_class,log_retention_days) VALUES('a','run-1',1,'test','failed',X'7b7d','hash',1,'gh:1','controller','execute','owner',1,2,'standard',7)`)
	id := RunEventSubscriptionID(tenant.team, "deliveries")
	if _, _, err := tenant.CreateRunEventSubscription(t.Context(), a, sealedSubscriptionFixture(t, tenant.team, id, "https://hooks.example/a", "whsec_x")); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	body := []byte(`{"id":"run-1/2"}`)
	if _, err := inTxResult(t.Context(), tenant.s, func(tx *storeTx) (struct{}, error) {
		return struct{}{}, insertRunEventDeliveryTx(t.Context(), tx, tenant.team, runEventDelivery{SubscriptionID: id, EventID: "run-1/2", RunID: "run-1", EventType: runevents.TypeFinished, Body: body}, now)
	}); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	var state string
	var deadline int64
	if err := tenant.s.db.QueryRow(`SELECT body,state,deadline_at FROM run_event_deliveries WHERE team='a' AND subscription_id=?`, id).Scan(&stored, &state, &deadline); err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(body) || state != "pending" || deadline != now.Add(RunEventDeliveryWindow).UnixNano() {
		t.Fatalf("delivery = %q %s %d", stored, state, deadline)
	}
	rewriteSchemaExec(t, tenant.s.db, `DELETE FROM runs WHERE id='run-1'`)
	rewriteSchemaCount(t, tenant.s.db, "run_event_deliveries", 0)
}
