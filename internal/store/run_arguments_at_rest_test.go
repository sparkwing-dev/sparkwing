package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

const (
	argSupplied            = "canary-supplied-7f3a9c41"
	argPlain               = "canary-plain-2b8e41d0"
	argDefault             = "canary-default-c91d5e33"
	argListA               = "canary-list-a-11d0e4f2"
	argListB               = "canary-list-b-22e0f5a3"
	argGenerated           = "canary-generated-5a7f6b04"
	argJobDefault          = "canary-job-default-64fe1a2b"
	argGeneratedJobDefault = "canary-generated-job-default-55ad3c4d"
)

var argumentCanaries = []string{argSupplied, argPlain, argDefault, argListA, argListB, argGenerated, argJobDefault, argGeneratedJobDefault}

func TestRunArgumentValuesNeverReachStoredRows(t *testing.T) {
	for _, dialect := range []struct {
		name   string
		target func(*testing.T) *storetest.Target
	}{{"sqlite", storetest.NewSQLite}, {"postgres", storetest.NewPostgres}} {
		t.Run(dialect.name, func(t *testing.T) {
			s := dialect.target(t).Open(t, store.WithPolicies(sealStorePolicies))
			checkRunArgumentsAtRest(t, s)
		})
	}
}

func checkRunArgumentsAtRest(t *testing.T, s *store.Store) {
	ctx := t.Context()
	source := admitArgumentRun(t, s, "acceptance", []nodewire.JobSpec{{ID: "alpha", Steps: []nodewire.StepSpec{{ID: "body"}}, Closures: nodewire.ClosureSpec{Generate: []string{"children"}}, Arguments: []nodewire.ArgumentDescription{jobArgument()}}})
	acceptArgumentExpansion(t, s, source)
	finished := admitArgumentRun(t, s, "retrier", []nodewire.JobSpec{})
	retry := retryArgumentRun(t, s, finished)

	for _, plan := range []store.PlanAcceptance{source, finished, retry} {
		tenant := teamOf(t, s, plan)
		row, err := tenant.ReadRun(ctx, plan.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if want := map[string]string{"key": argSupplied, "plain": argPlain}; !maps.Equal(row.Request.Request.RequestedArgs, want) {
			t.Fatalf("%s request arguments = %v", plan.RunID, row.Request.Request.RequestedArgs)
		}
		if row.Spec == nil || !maps.Equal(row.Spec.Resolved.Args, resolvedArguments()) || !maps.Equal(row.Spec.Request.Request.RequestedArgs, row.Request.Request.RequestedArgs) {
			t.Fatalf("%s spec arguments did not round trip: %+v", plan.RunID, row.Spec)
		}
		for _, want := range []string{argSupplied, argDefault, argListA, argListB, argJobDefault} {
			if !slices.Contains(row.Spec.Resolved.SecretRegistration.Values, want) {
				t.Fatalf("%s registration lost %q", plan.RunID, want)
			}
		}
		if got := *row.Spec.Resolved.ArgumentDeclarations[3].Default; got != argDefault {
			t.Fatalf("%s declaration default = %q", plan.RunID, got)
		}
	}
	row, err := teamOf(t, s, source).ReadRun(ctx, source.RunID)
	if err != nil {
		t.Fatal(err)
	}
	planHash, err := runspec.CanonicalHash(json.RawMessage(row.Plan))
	if err != nil || string(planHash) != row.PlanHash {
		t.Fatalf("plaintext plan hash changed: %v", err)
	}
	var plan nodewire.PlanDoc
	if json.Unmarshal(row.Plan, &plan) != nil || len(plan.Jobs) != 1 || *plan.Jobs[0].Arguments[0].Default != argJobDefault {
		t.Fatal("plan default did not round trip")
	}
	snapshot, err := teamOf(t, s, source).ReadSnapshot(ctx, source.RunID)
	if err != nil || len(snapshot.Expansions) != 1 || !slices.Contains(snapshot.Expansions[0].Registration.Values, argGenerated) || !slices.Contains(snapshot.Expansions[0].Registration.Values, argGeneratedJobDefault) {
		t.Fatalf("expansion registration did not round trip: %+v, %v", snapshot.Expansions, err)
	}

	for _, node := range snapshot.Nodes {
		var job nodewire.JobSpec
		if json.Unmarshal(node.Spec, &job) != nil || len(job.Arguments) != 1 || job.Arguments[0].Default == nil {
			t.Fatal("node default did not round trip")
		}
		want := argJobDefault
		if job.ID == "child" {
			want = argGeneratedJobDefault
		}
		if *job.Arguments[0].Default != want {
			t.Fatal("node default changed")
		}
		hash, err := runspec.CanonicalHash(job)
		if err != nil || string(hash) != node.SpecHash {
			t.Fatalf("plaintext node hash changed: %v", err)
		}
		read, err := teamOf(t, s, source).ReadNode(ctx, node.Ref)
		if err != nil || !bytes.Equal(read.Spec, node.Spec) {
			t.Fatalf("node transport bytes changed: %v", err)
		}
	}
	var expansion nodewire.PlanDoc
	if json.Unmarshal(snapshot.Expansions[0].Body, &expansion) != nil || len(expansion.Jobs) != 1 || *expansion.Jobs[0].Arguments[0].Default != argGeneratedJobDefault {
		t.Fatal("expansion default did not round trip")
	}

	if leaks := storedCanaries(t, s, argumentCanaries); len(leaks) > 0 {
		t.Fatalf("argument values stored in plaintext:\n%s", strings.Join(leaks, "\n"))
	}

	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s, `UPDATE runs SET request_json=(SELECT request_json FROM runs WHERE id=?) WHERE id=?`), source.RunID, retry.RunID); err != nil {
		t.Fatal(err)
	}
	_, err = teamOf(t, s, retry).ReadRun(ctx, retry.RunID)
	var refused *apierr.Error
	if !errors.As(err, &refused) || refused.Code != hosterr.IntegrityFailure || strings.Contains(err.Error(), argSupplied) {
		t.Fatalf("another run's sealed arguments opened: %v", err)
	}
}

func resolvedArguments() map[string]string {
	return map[string]string{"jtok": argJobDefault, "key": argSupplied, "keys": argListA + "," + argListB, "plain": argPlain, "token": argDefault}
}

func jobArgument() nodewire.ArgumentDescription {
	value := argJobDefault
	return nodewire.ArgumentDescription{Name: "jtok", Type: "string", Secret: true, Default: &value, JobID: "alpha"}
}

func teamOf(t *testing.T, s *store.Store, plan store.PlanAcceptance) *store.Tenant {
	t.Helper()
	tenant, err := s.ForTeam(t.Context(), store.Team(plan.Seal.Request.Request.Team))
	if err != nil {
		t.Fatal(err)
	}
	return tenant
}

// safety: one supplied argument is classified and one is not, so sealing cannot depend on classification.
func admitArgumentRun(t *testing.T, s *store.Store, team string, jobs []nodewire.JobSpec) store.PlanAcceptance {
	t.Helper()
	tenant, plan, err := storePlanSealRequestFor(t.Context(), s, team, false, func(in *store.IntakeRequest, doc *nodewire.PlanDoc) {
		in.Request.RequestedArgs = map[string]string{"key": argSupplied, "plain": argPlain}
		in.Request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{"key"}}
		doc.Jobs = jobs
	})
	if err != nil {
		t.Fatal(err)
	}
	list, value := argListA+","+argListB, argDefault
	plan.Seal.Resolved.ArgumentDeclarations = []nodewire.ArgumentDescription{
		{Name: "key", Type: "string", Secret: true},
		{Name: "keys", Type: "[]string", Secret: true, Default: &list},
		{Name: "plain", Type: "string"},
		{Name: "token", Type: "string", Secret: true, Default: &value},
		jobArgument(),
	}
	plan.Seal.Resolved.Args = resolvedArguments()
	plan.Seal.Resolved.SecretArgs = []string{"jtok", "key", "keys", "token"}
	plan.Seal.Resolved.SecretRegistration = runspec.SecretRegistration{Defaults: []runspec.SecretValue{{Raw: argDefault}, {Raw: list, List: true}, {Raw: argJobDefault}}, ListArgs: []string{"keys"}}
	if _, err := tenant.AcceptPlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func acceptArgumentExpansion(t *testing.T, s *store.Store, plan store.PlanAcceptance) {
	t.Helper()
	ctx := t.Context()
	tenant := teamOf(t, s, plan)
	source, err := finishGeneratedDependencyJob(ctx, s, tenant, plan, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.AsOperator().ResolveAuthority(ctx, plan.Actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	binding := owner.Capability.Binding
	binding.Kind, binding.NodeID, binding.Generation = capability.BindingNode, "alpha", int64(source.Generation)
	binding.Expansion = &capability.LeaseRef{Kind: capability.UnitTrigger, ID: plan.RunID, Holder: plan.Actor.Owner.Holder, Generation: int64(plan.Actor.Owner.Generation)}
	expander, err := s.AsOperator().DelegateCapability(ctx, capability.DelegateSpec{
		Parent: plan.Actor.Authority, Audience: capability.Controller, Grants: []capability.Grant{capability.ExpansionSubmit},
		Binding: binding, ExpiresAt: owner.Capability.ExpiresAt, Repositories: owner.Capability.Repositories,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := store.ExpansionSubmission{
		Registration: runspec.SecretRegistration{Version: 1, Defaults: []runspec.SecretValue{{Raw: argGenerated}, {Raw: argGeneratedJobDefault}}},
		Source:       source.Ref, SourceGeneration: source.Generation,
		Actor: runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: expander.Capability.Prefix}, CapabilityPrefix: expander.Capability.Prefix},
		Body:  []byte(`{"jobs":[{"id":"child","groups":["children"],"deps":["alpha"],"steps":[{"id":"body"}],"arguments":[{"name":"gtok","type":"string","required":false,"default":"` + argGeneratedJobDefault + `","secret":true,"job_id":"child"}]}]}`),
	}
	if _, err := tenant.AcceptExpansion(ctx, request); err != nil {
		t.Fatal(err)
	}
}

func retryArgumentRun(t *testing.T, s *store.Store, plan store.PlanAcceptance) store.PlanAcceptance {
	t.Helper()
	ctx := t.Context()
	tenant := teamOf(t, s, plan)
	if _, err := tenant.TransitionRun(ctx, runstate.RunTransition{RunID: runspec.RunID(plan.RunID), Actor: plan.Actor, To: runstate.RunRunning, Cause: runstate.RunStart}); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.FinishRunAndUnit(ctx, runstate.FinishRunRequest{Unit: plan.Actor.Owner, Transition: runstate.RunCompletion{RunID: runspec.RunID(plan.RunID), Cause: runstate.RunFinish, Actor: plan.Actor}}); err != nil {
		t.Fatal(err)
	}
	source, err := tenant.ReadRun(ctx, plan.RunID)
	if err != nil || source.Spec == nil {
		t.Fatalf("finished source: %+v, %v", source.Spec, err)
	}
	owner, err := s.AsOperator().ResolveAuthority(ctx, plan.Actor.Authority)
	if err != nil || owner.Capability.Parent == nil {
		t.Fatalf("owner ancestry: %+v, %v", owner, err)
	}
	admitted, err := tenant.AdmitRun(ctx, store.IntakeRequest{Authority: source.Request.AuthorityRef, Request: runspec.Request{Lineage: runspec.Lineage{RetryOf: plan.RunID}}})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: owner.Capability.Parent.ID, Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: runspec.RunID(admitted.RunID)}})
	if err != nil {
		t.Fatal(err)
	}
	actor := runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}
	retry := store.PlanAcceptance{RunID: admitted.RunID, Actor: actor, Seal: runspec.SealInput{Request: admitted.Request, Declarations: source.Spec.Declarations, Resolved: source.Spec.Resolved, Plan: source.Plan}}
	if _, err := tenant.AcceptPlan(ctx, retry); err != nil {
		t.Fatal(err)
	}
	return retry
}

// safety: every column of every table is read as raw bytes, so a new storage location cannot hide a canary.
func storedCanaries(t *testing.T, s *store.Store, canaries []string) []string {
	t.Helper()
	ctx := t.Context()
	query := `SELECT name FROM sqlite_master WHERE type='table'`
	if s.Dialect() == store.DialectPostgres {
		query = `SELECT table_name FROM information_schema.tables WHERE table_schema=current_schema() AND table_type='BASE TABLE'`
	}
	tables, err := queryStrings(ctx, s.DB(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) < 10 {
		t.Fatalf("scanned only %d tables", len(tables))
	}
	var forms [][]byte
	for _, canary := range canaries {
		forms = append(forms, []byte(canary), []byte(base64.StdEncoding.EncodeToString([]byte(canary))))
	}
	var leaks []string
	for _, table := range tables {
		found, err := tableCanaries(ctx, s.DB(), table, forms)
		if err != nil {
			t.Fatal(err)
		}
		leaks = append(leaks, found...)
	}
	return leaks
}

func tableCanaries(ctx context.Context, db *sql.DB, table string, forms [][]byte) (_ []string, err error) {
	rows, err := db.QueryContext(ctx, `SELECT * FROM "`+table+`"`)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var leaks []string
	for rows.Next() {
		cells := make([]sql.RawBytes, len(columns))
		targets := make([]any, len(cells))
		for i := range cells {
			targets[i] = &cells[i]
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		for i, cell := range cells {
			for _, form := range forms {
				if bytes.Contains(cell, form) {
					leaks = append(leaks, fmt.Sprintf("%s.%s: %s", table, columns[i], form))
				}
			}
		}
	}
	return leaks, rows.Err()
}

func queryStrings(ctx context.Context, db *sql.DB, query string) (_ []string, err error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

type refusingRunCipher struct{}

func (refusingRunCipher) SealBound(secrets.Purpose, string, string, string, bool, bool, string) (string, error) {
	return "", fmt.Errorf("key file outside the home: %w", secrets.ErrKeyRefused)
}

func (refusingRunCipher) OpenBound(secrets.Purpose, string, string, string, bool, bool, string) (string, error) {
	return "", fmt.Errorf("key file outside the home: %w", secrets.ErrKeyRefused)
}

type keylessRunCipher struct{ refusingRunCipher }

func (keylessRunCipher) Keyless() {}

func TestRunArgumentsFailClosedUnlessTheStoreIsKeyless(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cipher store.SessionKeyCipher
	}{{"refused key", refusingRunCipher{}}, {"keyless", keylessRunCipher{}}} {
		t.Run(tc.name, func(t *testing.T) {
			s := storetest.NewSQLite(t).Open(t, store.WithPolicies(func(*store.Store) (store.Policies, error) {
				return store.Policies{ClaimProfiles: acceptanceClaimProfile{}, RunEnvironment: tc.cipher}, nil
			}))
			tenant, plan, err := storePlanSealRequestWith(t.Context(), s, func(in *store.IntakeRequest, _ *nodewire.PlanDoc) {
				in.Request.RequestedArgs = map[string]string{"key": argSupplied}
				in.Request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{"key"}}
			})
			leaks := storedCanaries(t, s, argumentCanaries)
			if _, keyless := tc.cipher.(store.KeylessCipher); !keyless {
				var refused *apierr.Error
				if !errors.As(err, &refused) || refused.Code != hosterr.Unavailable || len(leaks) != 0 {
					t.Fatalf("a keyed store that cannot seal admitted the run: %v, leaks %v", err, leaks)
				}
				return
			}
			if err != nil || len(leaks) == 0 {
				t.Fatalf("keyless admission: %v, leaks %v", err, leaks)
			}
			row, err := tenant.ReadRun(t.Context(), plan.RunID)
			if err != nil || row.Request.Request.RequestedArgs["key"] != argSupplied {
				t.Fatalf("keyless run did not read back: %v, %v", row.Request.Request.RequestedArgs, err)
			}
		})
	}
}
