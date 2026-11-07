package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

const localRunnerSubject = "uid:1000"

func localRunnerStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func localRunnerInput(t *testing.T, s *Store, id string) LocalRunnerInstance {
	t.Helper()
	auth, err := s.AsOperator().ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialPeer, Peer: capability.Subject{Kind: capability.SubjectLocal, ID: localRunnerSubject}}, capability.RequestEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: auth.Authorities[0].Principal.Authority, Audience: capability.Controller, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone}})
	if err != nil {
		t.Fatal(err)
	}
	return LocalRunnerInstance{ID: id, CapabilityPrefix: child.Capability.Prefix, PID: 123, ProcessBirth: "birth", Version: "test-version", BuildIdentity: "test-build"}
}

func registerLocalRunner(t *testing.T, s *Store, id string) LocalRunnerInstance {
	t.Helper()
	instance, err := s.AsOperator().RegisterLocalRunner(t.Context(), localRunnerSubject, localRunnerInput(t, s, id))
	if err != nil {
		t.Fatal(err)
	}
	return instance
}

func TestLocalRunnerIdentityAndStateSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	input := localRunnerInput(t, s, "instance")
	first, err := s.AsOperator().RegisterLocalRunner(t.Context(), localRunnerSubject, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != "registered" || first.Machine == "" {
		t.Fatalf("registered: %+v", first)
	}
	if err := s.AsOperator().ActivateLocalRunner(t.Context(), localRunnerSubject, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	got, err := s.AsOperator().ActiveLocalRunner(t.Context(), localRunnerSubject)
	first.State = "active"
	if err != nil || got != first {
		t.Fatalf("reopened: %+v, %v; want %+v", got, err, first)
	}
	repeat, err := s.AsOperator().RegisterLocalRunner(t.Context(), localRunnerSubject, input)
	if err != nil || repeat != first {
		t.Fatalf("repeat: %+v, %v", repeat, err)
	}
	input.PID++
	if _, err := s.AsOperator().RegisterLocalRunner(t.Context(), localRunnerSubject, input); err == nil {
		t.Fatal("conflicting process identity accepted")
	}
}

func TestLocalRunnerRegistrationRejectsInvalidIdentityAndAuthority(t *testing.T) {
	s := localRunnerStore(t)
	input := localRunnerInput(t, s, "valid")
	rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('foreign',1,1)`)
	localRunnerTrigger(t, s, "run", 1)
	for name, change := range map[string]func(*LocalRunnerInstance){
		"missing ID":        func(i *LocalRunnerInstance) { i.ID = "" },
		"missing PID":       func(i *LocalRunnerInstance) { i.PID = 0 },
		"missing birth":     func(i *LocalRunnerInstance) { i.ProcessBirth = " " },
		"missing version":   func(i *LocalRunnerInstance) { i.Version = "" },
		"missing build":     func(i *LocalRunnerInstance) { i.BuildIdentity = "" },
		"forged state":      func(i *LocalRunnerInstance) { i.State = "active" },
		"forged machine":    func(i *LocalRunnerInstance) { i.Machine = "foreign" },
		"missing authority": func(i *LocalRunnerInstance) { i.CapabilityPrefix = "missing" },
	} {
		t.Run(name, func(t *testing.T) {
			modified := input
			change(&modified)
			if _, err := s.AsOperator().RegisterLocalRunner(t.Context(), localRunnerSubject, modified); err == nil {
				t.Fatal("accepted invalid registration")
			}
		})
	}
	if _, err := s.AsOperator().RegisterLocalRunner(t.Context(), "uid:2000", input); err == nil {
		t.Fatal("accepted foreign subject")
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: input.CapabilityPrefix}
	p, err := s.AsOperator().ResolveAuthority(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*capability.Capability){
		"extra grant":     func(c *capability.Capability) { c.Grants = append(c.Grants, capability.RunsWrite) },
		"missing grant":   func(c *capability.Capability) { c.Grants = []capability.Grant{capability.NodesClaim} },
		"foreign team":    func(c *capability.Capability) { c.Team = "foreign" },
		"machine subject": func(c *capability.Capability) { c.Subject.Kind = capability.SubjectMachine },
		"bound": func(c *capability.Capability) {
			c.Binding = capability.Binding{Kind: capability.BindingRun, RunID: "run"}
		},
		"audience":          func(c *capability.Capability) { c.Audience = capability.NodeHost },
		"missing peer root": func(c *capability.Capability) { c.Parent = nil },
	} {
		t.Run(name, func(t *testing.T) {
			c := p.Capability
			c.Prefix = "invalid-" + name
			c.Digest = sha256.Sum256([]byte(c.Prefix))
			c.Grants = append([]capability.Grant(nil), c.Grants...)
			change(&c)
			authorityPersist(t, s, c)
			modified := input
			modified.CapabilityPrefix = c.Prefix
			if _, err := s.AsOperator().RegisterLocalRunner(t.Context(), localRunnerSubject, modified); err == nil {
				t.Fatal("accepted nonlocal claim authority")
			}
		})
	}
	rewriteSchemaCount(t, s.db, "local_runner_instances", 0)
}

func localRunnerTrigger(t *testing.T, s *Store, id string, number int) UnitDemand {
	t.Helper()
	request := sealWith(t, runspec.SealedRequest{Request: runspec.Request{Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "https://github.com/acme/repo"}}}}, assertedProvenance(t, "ci", "", ""))
	rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days) VALUES ('default',?,?,'ci','pending',?,'request',1,'gh:1','https://github.com/acme/repo','host','execute','local',1,'r30',30)`, id, number, request)
	rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,needs_labels,prefers_labels) VALUES ('default',?,'trigger',1,1,?,?)`, id, []byte(`[]`), []byte(`[]`))
	indexFixtureUnit(t, s, DefaultTeam, UnitRef{Kind: capability.UnitTrigger, RunID: runspec.RunID(id)})
	return UnitDemand{Target: &UnitRef{Kind: capability.UnitTrigger, RunID: runspec.RunID(id)}}
}

func TestLocalRunnerRollFencesClaimsAndPreservesHeldAward(t *testing.T) {
	s := localRunnerStore(t)
	first := registerLocalRunner(t, s, "first")
	second := registerLocalRunner(t, s, "second")
	if first.Machine != second.Machine {
		t.Fatal("same peer got different machines")
	}
	demand := localRunnerTrigger(t, s, "run", 1)
	demand.CapabilityPrefix = first.CapabilityPrefix
	if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); !errors.Is(err, ErrNotFound) {
		t.Fatalf("registered claim: %v", err)
	}
	queued := demand
	queued.Target, queued.Kinds = nil, []capability.UnitKind{capability.UnitTrigger}
	if _, err := s.AsOperator().ClaimUnit(t.Context(), queued); !errors.Is(err, ErrNotFound) {
		t.Fatalf("registered queue claim: %v", err)
	}
	if err := s.AsOperator().ActivateLocalRunner(t.Context(), localRunnerSubject, first.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AsOperator().ActivateLocalRunner(t.Context(), localRunnerSubject, second.ID); err != nil {
		t.Fatal(err)
	}
	old, err := s.AsOperator().ReadLocalRunner(t.Context(), localRunnerSubject, first.ID)
	if err != nil || old.State != "draining" {
		t.Fatalf("old instance: %+v, %v", old, err)
	}
	next := localRunnerTrigger(t, s, "next", 2)
	next.CapabilityPrefix = first.CapabilityPrefix
	if _, err := s.AsOperator().ClaimUnit(t.Context(), next); !errors.Is(err, ErrNotFound) {
		t.Fatalf("draining claim: %v", err)
	}
	queued = next
	queued.Target, queued.Kinds = nil, []capability.UnitKind{capability.UnitTrigger}
	if _, err := s.AsOperator().ClaimUnit(t.Context(), queued); !errors.Is(err, ErrNotFound) {
		t.Fatalf("draining queue claim: %v", err)
	}
	next.CapabilityPrefix = second.CapabilityPrefix
	if _, err := s.AsOperator().ClaimUnit(t.Context(), next); err != nil {
		t.Fatal("successor claim:", err)
	}
	if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
		t.Fatal("drained owner renewal:", err)
	}
	actor := runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}
	tenant := &Tenant{s: s, team: DefaultTeam}
	if _, err := tenant.FinishRunAndUnit(t.Context(), runstate.FinishRunRequest{Unit: claim.Owner, Transition: runstate.RunCompletion{RunID: runspec.RunID(claim.RunID), Cause: runstate.RunFinish, Actor: actor}}); err != nil {
		t.Fatal("drained owner finish:", err)
	}
}

func TestLocalRunnerLifecycleIsMonotonicAndSubjectScoped(t *testing.T) {
	s := localRunnerStore(t)
	instance := registerLocalRunner(t, s, "instance")
	operations := []func() error{
		func() error { return s.AsOperator().ActivateLocalRunner(t.Context(), "uid:2000", instance.ID) },
		func() error { return s.AsOperator().DrainLocalRunner(t.Context(), "uid:2000", instance.ID) },
		func() error { return s.AsOperator().StopLocalRunner(t.Context(), "uid:2000", instance.ID) },
		func() error {
			_, err := s.AsOperator().ReadLocalRunner(t.Context(), "uid:2000", instance.ID)
			return err
		},
	}
	for i, operation := range operations {
		if err := operation(); err == nil {
			t.Fatalf("foreign operation %d accepted", i)
		}
	}
	for _, state := range []string{"active", "active", "draining", "draining", "stopped", "stopped", "draining"} {
		if err := s.AsOperator().changeLocalRunner(t.Context(), localRunnerSubject, instance.ID, state); err != nil {
			t.Fatalf("state %s: %v", state, err)
		}
		if state == "draining" || state == "stopped" {
			if err := s.AsOperator().ActivateLocalRunner(t.Context(), localRunnerSubject, instance.ID); err == nil {
				t.Fatal("reactivated retired instance")
			}
		}
	}
	got, err := s.AsOperator().ReadLocalRunner(t.Context(), localRunnerSubject, instance.ID)
	if err != nil || got.State != "stopped" {
		t.Fatalf("final state: %+v, %v", got, err)
	}
	if err := s.AsOperator().DrainLocalRunner(t.Context(), localRunnerSubject, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing instance: %v", err)
	}
}

func TestLocalRunnerConcurrentActivationKeepsOneActiveInstance(t *testing.T) {
	s := localRunnerStore(t)
	const count = 8
	instances := make([]LocalRunnerInstance, count)
	for i := range instances {
		instances[i] = registerLocalRunner(t, s, fmt.Sprintf("instance-%d", i))
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, instance := range instances {
		wg.Go(func() {
			<-start
			if err := s.AsOperator().ActivateLocalRunner(t.Context(), localRunnerSubject, instance.ID); err != nil {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	var active, draining int
	for _, instance := range instances {
		got, err := s.AsOperator().ReadLocalRunner(t.Context(), localRunnerSubject, instance.ID)
		if err != nil {
			t.Fatal(err)
		}
		switch got.State {
		case "active":
			active++
		case "draining":
			draining++
		default:
			t.Fatalf("unexpected state %s", got.State)
		}
	}
	if active != 1 || draining != count-1 {
		t.Fatalf("active=%d draining=%d", active, draining)
	}
}

func TestLocalRunnerStoppedIdentityRetiresWithItsEndedCredential(t *testing.T) {
	s := localRunnerStore(t)
	instance := registerLocalRunner(t, s, "instance")
	if err := s.AsOperator().StopLocalRunner(t.Context(), localRunnerSubject, instance.ID); err != nil {
		t.Fatal(err)
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: instance.CapabilityPrefix}
	principal, err := s.AsOperator().ResolveAuthority(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Tenant{s: s, team: DefaultTeam}).RevokeAuthority(t.Context(), runstate.Actor{Authority: *principal.Capability.Parent}, ref); err != nil {
		t.Fatal(err)
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		count, err := tx.operator.deleteRetentionCapabilitiesTx(t.Context(), tx, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC), 100)
		if err == nil && count != 1 {
			return fmt.Errorf("removed %d capabilities, want one", count)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().ReadLocalRunner(t.Context(), localRunnerSubject, instance.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retired instance: %v", err)
	}
}

func TestActiveLocalRunnerFollowsRollAndHidesForeignSubjects(t *testing.T) {
	s := localRunnerStore(t)
	first := registerLocalRunner(t, s, "first")
	second := registerLocalRunner(t, s, "second")
	if _, err := s.AsOperator().ActiveLocalRunner(t.Context(), localRunnerSubject); !errors.Is(err, ErrNotFound) {
		t.Fatalf("registered discovery: %v", err)
	}
	for _, instance := range []LocalRunnerInstance{first, second} {
		if err := s.AsOperator().ActivateLocalRunner(t.Context(), localRunnerSubject, instance.ID); err != nil {
			t.Fatal(err)
		}
		got, err := s.AsOperator().ActiveLocalRunner(t.Context(), localRunnerSubject)
		instance.State = "active"
		if err != nil || got != instance {
			t.Fatalf("active: %+v %v; want %+v", got, err, instance)
		}
		if _, err := s.AsOperator().ActiveLocalRunner(t.Context(), "uid:2000"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign discovery: %v", err)
		}
	}
	if err := s.AsOperator().DrainLocalRunner(t.Context(), localRunnerSubject, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().ActiveLocalRunner(t.Context(), localRunnerSubject); !errors.Is(err, ErrNotFound) {
		t.Fatalf("draining discovery: %v", err)
	}
}

func TestActiveLocalRunnerIgnoresRetiredPeerRoot(t *testing.T) {
	s := localRunnerStore(t)
	old := registerLocalRunner(t, s, "old")
	if err := s.AsOperator().ActivateLocalRunner(t.Context(), localRunnerSubject, old.ID); err != nil {
		t.Fatal(err)
	}
	s.credentialEpoch += "-rotated"
	current := registerLocalRunner(t, s, "current")
	if current.Machine == old.Machine {
		t.Fatal("credential epoch retained the old peer root")
	}
	if err := s.AsOperator().ActivateLocalRunner(t.Context(), localRunnerSubject, current.ID); err != nil {
		t.Fatal(err)
	}
	current.State = "active"
	got, err := s.AsOperator().ActiveLocalRunner(t.Context(), localRunnerSubject)
	if err != nil || got != current {
		t.Fatalf("current epoch discovery: %+v %v; want %+v", got, err, current)
	}
	var state string
	if err := s.db.QueryRowContext(t.Context(), `SELECT state FROM local_runner_instances WHERE id=?`, old.ID).Scan(&state); err != nil || state != "active" {
		t.Fatalf("old root fixture no longer active: %s %v", state, err)
	}
}

func TestActiveLocalRunnerReplacesInstanceWithRevokedCredential(t *testing.T) {
	s := localRunnerStore(t)
	old := registerLocalRunner(t, s, "old")
	if err := s.AsOperator().ActivateLocalRunner(t.Context(), localRunnerSubject, old.ID); err != nil {
		t.Fatal(err)
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: old.CapabilityPrefix}
	principal, err := s.AsOperator().ResolveAuthority(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Tenant{s: s, team: DefaultTeam}).RevokeAuthority(t.Context(), runstate.Actor{Authority: *principal.Capability.Parent}, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().ActiveLocalRunner(t.Context(), localRunnerSubject); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked active runner discovery: %v", err)
	}
	replacement := registerLocalRunner(t, s, "replacement")
	if err := s.AsOperator().ActivateLocalRunner(t.Context(), localRunnerSubject, replacement.ID); err != nil {
		t.Fatal(err)
	}
	replacement.State = "active"
	if got, err := s.AsOperator().ActiveLocalRunner(t.Context(), localRunnerSubject); err != nil || got != replacement {
		t.Fatalf("replacement discovery: %+v %v; want %+v", got, err, replacement)
	}
	var state string
	if err := s.db.QueryRowContext(t.Context(), `SELECT state FROM local_runner_instances WHERE id=?`, old.ID).Scan(&state); err != nil || state != "draining" {
		t.Fatalf("revoked runner row was not retired: %s %v", state, err)
	}
}
