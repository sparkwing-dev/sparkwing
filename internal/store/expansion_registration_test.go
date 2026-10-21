package store

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestExpansionRegistrationPersistsOutsideGeneratedPlanAndBindsReplay(t *testing.T) {
	tenant, request := successfulExpansionFixture(t)
	request.Registration.Defaults = []runspec.SecretValue{{Raw: "first-private,\u00a0second-private", List: true}}
	accepted, err := tenant.AcceptExpansion(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := tenant.ReadSnapshot(t.Context(), string(request.Source.RunID))
	if err != nil || len(snapshot.Expansions) != 1 {
		t.Fatalf("accepted snapshot: %+v, %v", snapshot.Expansions, err)
	}
	projection := snapshot.Expansions[0].Registration
	for _, want := range []string{"first-private", "second-private", "first-private,\u00a0second-private"} {
		if !slices.Contains(projection.Values, want) {
			t.Errorf("missing registration %q", want)
		}
	}
	if strings.Contains(string(snapshot.Expansions[0].Body), "private") {
		t.Fatal("private registration entered generated plan")
	}
	replayed, err := tenant.AcceptExpansion(t.Context(), request)
	if err != nil || !replayed.Replayed || replayed.BodyHash != accepted.BodyHash {
		t.Fatalf("identical expansion replay: %+v, %v", replayed, err)
	}
	request.Registration.Defaults[0].Raw = "changed-private"
	if _, err := tenant.AcceptExpansion(t.Context(), request); err == nil {
		t.Fatal("changed registration replayed")
	}
	projection.Defaults[0].Raw = "changed-private"
	projection.Values = nil
	projection, err = runspec.BuildSecretRegistration(runspec.Request{}, runspec.Resolved{SecretRegistration: projection})
	if err != nil {
		t.Fatal(err)
	}
	corrupt, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET expansion_registration=? WHERE run_id=? AND node_id=?`, corrupt, request.Source.RunID, request.Source.NodeID)
	if _, err := tenant.ReadSnapshot(t.Context(), string(request.Source.RunID)); err == nil {
		t.Fatal("changed persisted registration passed the accepted digest")
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET expansion_registration=NULL WHERE run_id=? AND node_id=?`, request.Source.RunID, request.Source.NodeID)
	if _, err := tenant.ReadSnapshot(t.Context(), string(request.Source.RunID)); err == nil {
		t.Fatal("missing persisted registration passed as an empty projection")
	}
}

func TestExpansionRegistrationRefusesShortItem(t *testing.T) {
	tenant, request := successfulExpansionFixture(t)
	request.Registration.Defaults = []runspec.SecretValue{{Raw: "ab,long-private", List: true}}
	if _, err := tenant.AcceptExpansion(t.Context(), request); err == nil || !strings.Contains(err.Error(), "plain config variable") {
		t.Fatalf("expected short-item refusal, got %v", err)
	}
}
