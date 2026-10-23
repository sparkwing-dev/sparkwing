package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

func TestExpansionRequiresExplicitVersionedRegistration(t *testing.T) {
	for _, version := range []int{0, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			tenant, request := successfulExpansionFixture(t)
			request.Registration = runspec.SecretRegistration{Version: version}
			_, err := tenant.AcceptExpansion(t.Context(), request)
			var problem *apierr.Error
			if !errors.As(err, &problem) || problem.Code != apierr.PlanInvalid || !strings.Contains(problem.Message, "version 1 secret registration") {
				t.Fatalf("unversioned expansion was not refused: %v", err)
			}
			row, err := tenant.ReadNode(t.Context(), request.Source)
			if err != nil || row.Expansion != runstate.ExpansionExpected {
				t.Fatalf("refusal changed expansion state: %+v, %v", row, err)
			}
		})
	}
}

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

func TestControlTextIncludesAcceptedExpansionRegistrations(t *testing.T) {
	tenant, request := successfulExpansionFixture(t)
	request.Registration.Defaults = []runspec.SecretValue{{Raw: "generated-private-token"}}
	if _, err := tenant.AcceptExpansion(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	err := tenant.s.inTx(t.Context(), func(tx *storeTx) error {
		run, err := readRunTx(t.Context(), tx, tenant.team, string(request.Source.RunID))
		if err != nil {
			return err
		}
		got, err := controlTextTx(t.Context(), tx, tenant.team, run, "reason generated-private-token")
		if err != nil {
			return err
		}
		if got != "reason ***" {
			t.Errorf("generated registration omitted from control text: %q", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedArgumentClassificationReachesEveryRunPresentation(t *testing.T) {
	tenant, request := successfulExpansionFixture(t)
	run, err := tenant.ReadRun(t.Context(), string(request.Source.RunID))
	if err != nil {
		t.Fatal(err)
	}
	spec := *run.Spec
	spec.Request.Request.RequestedArgs = map[string]string{"dynamic": "generated-private-token", "public": "public-value"}
	spec.Resolved.Args = map[string]string{"dynamic": "generated-private-token", "public": "public-value"}
	spec.Resolved.SecretArgs = []string{}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET spec_json=? WHERE team=? AND id=?`, raw, tenant.team, request.Source.RunID)
	request.Registration.Defaults = []runspec.SecretValue{{Raw: "generated-private-token"}}
	if _, err := tenant.AcceptExpansion(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	row, err := tenant.ReadRun(t.Context(), string(request.Source.RunID))
	if err != nil {
		t.Fatal(err)
	}
	display := row.Display()
	if display.RequestedArgs["dynamic"] != "***" || display.Args["dynamic"] != "***" || display.Args["public"] != "public-value" || strings.Contains(strings.Join(display.Reproducer, " "), "generated-private-token") {
		t.Fatalf("unsafe dynamic display: %+v", display)
	}
	unchanged, err := json.Marshal(row.Spec)
	if err != nil || string(unchanged) != string(raw) {
		t.Fatalf("presentation changed sealed bytes: %v", err)
	}
	direct, err := tenant.GetRun(t.Context(), string(request.Source.RunID))
	if err != nil {
		t.Fatal(err)
	}
	listed, err := tenant.ListRuns(t.Context(), RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, shown := range append(listed, direct, latestRunPresentation(row)) {
		if shown.Args["dynamic"] != "***" || shown.Args["public"] != "public-value" {
			t.Fatalf("unsafe persisted run presentation: %+v", shown.Args)
		}
	}
	trigger, err := tenant.GetTrigger(t.Context(), string(request.Source.RunID))
	if err != nil {
		t.Fatal(err)
	}
	triggers, err := tenant.ListTriggers(t.Context(), TriggerFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, shown := range append(triggers, trigger) {
		if shown.Args["dynamic"] != "***" || shown.Args["public"] != "public-value" {
			t.Fatalf("unsafe trigger presentation: %+v", shown.Args)
		}
	}
}
