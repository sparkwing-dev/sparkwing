package acceptance_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// safety: independent alternatives judge policy selection without claiming store or transport evidence.
func TestCredentialPolicyAlternatives(t *testing.T) {
	for _, route := range capability.Routes() {
		if route.Authentication != capability.AuthCapability {
			continue
		}
		t.Run(route.Method+" "+route.Path, func(t *testing.T) {
			for _, access := range route.Access {
				grants := make([]string, len(access.Grants))
				for i, grant := range access.Grants {
					grants[i] = string(grant)
				}
				alternative := strings.Join([]string{string(access.Credential), strings.Join(grants, ","), string(access.Binding), string(access.Liveness)}, "|")
				t.Run(alternative, func(t *testing.T) { checkPolicyAlternative(t, route, string(route.Audience), alternative) })
			}
		})
	}
}

func checkPolicyAlternative(t *testing.T, route capability.RoutePolicy, audience, alternative string) {
	t.Helper()
	parts := strings.Split(alternative, "|")
	var grants []capability.Grant
	if parts[1] != "" {
		for _, grant := range strings.Split(parts[1], ",") {
			grants = append(grants, capability.Grant(grant))
		}
	}
	index := slices.IndexFunc(route.Access, func(rule capability.AccessRule) bool {
		return string(rule.Credential) == parts[0] && string(rule.Binding) == parts[2] && string(rule.Liveness) == parts[3] && slices.Equal(rule.Grants, grants)
	})
	if index < 0 {
		t.Fatal("complete alternative is missing")
	}
	p, target := policyFixture(parts[0], parts[2], audience, grants)
	if parts[2] == "claimSelection" {
		target.ClaimKinds = []capability.UnitKind{capability.UnitNode}
		if slices.Contains(grants, capability.TriggersClaim) {
			target.ClaimKinds = []capability.UnitKind{capability.UnitTrigger}
		}
	}
	auth := capability.Authenticated{Principal: p, Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: index}}}
	authorized, err := capability.Authorize(context.Background(), policyBinding{}, route, auth, target)
	if err != nil || authorized.Access.Credential != capability.CredentialRule(parts[0]) {
		t.Fatalf("positive alternative: %+v %v", authorized.Access, err)
	}
	t.Run("wrong audience", func(t *testing.T) {
		copy := auth
		copy.Principal.Capability.Audience = "other"
		_, err := capability.Authorize(context.Background(), policyBinding{}, route, copy, target)
		assertPolicyCode(t, err, apierr.Forbidden)
	})
	t.Run("dead", func(t *testing.T) {
		copy := auth
		copy.Live = nil
		_, err := capability.Authorize(context.Background(), policyBinding{}, route, copy, target)
		assertPolicyCode(t, err, apierr.Unauthenticated)
	})
	t.Run("binding refusal", func(t *testing.T) {
		_, err := capability.Authorize(context.Background(), policyBinding{refuse: true}, route, auth, target)
		assertPolicyCode(t, err, apierr.Forbidden)
	})
	if parts[0] == "operator_console_session" {
		t.Run("deployment bearer", func(t *testing.T) {
			copy := auth
			copy.Principal.Class = capability.CredentialBearer
			_, err := capability.Authorize(context.Background(), policyBinding{}, route, copy, target)
			assertPolicyCode(t, err, apierr.Forbidden)
		})
		t.Run("local peer", func(t *testing.T) {
			copy := auth
			copy.Principal.Class = capability.CredentialPeer
			copy.Principal.Capability.Subject.Kind = capability.SubjectLocal
			_, err := capability.Authorize(context.Background(), policyBinding{}, route, copy, target)
			assertPolicyCode(t, err, apierr.Forbidden)
		})
	}
	for i, grant := range grants {
		t.Run("missing "+string(grant), func(t *testing.T) {
			copy := auth
			copy.Principal.Capability.Grants = slices.Delete(slices.Clone(grants), i, i+1)
			_, err := capability.Authorize(context.Background(), policyBinding{}, route, copy, target)
			assertPolicyCode(t, err, apierr.Forbidden)
		})
	}
}

type policyBinding struct{ refuse bool }

func (b policyBinding) CheckBinding(context.Context, capability.Principal, capability.BindingRule, capability.Target) error {
	if b.refuse {
		return &apierr.Error{Code: apierr.Forbidden, Message: "fixture relation refused", Details: map[string]any{"reason": "binding"}}
	}
	return nil
}

func assertPolicyCode(t *testing.T, err error, want apierr.Code) {
	t.Helper()
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != want {
		t.Fatalf("error=%v want=%s", err, want)
	}
}

func policyFixture(credential, binding, audience string, grants []capability.Grant) (capability.Principal, capability.Target) {
	p := capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "credential"}, Capability: capability.Capability{
		Prefix: "credential", Team: "team", Audience: capability.Audience(audience), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "account"},
		Issuance: capability.Delegate, Grants: grants, Binding: capability.Binding{Kind: capability.BindingNone},
	}}
	target := capability.Target{Team: "team", RunID: "run", NodeID: "job", BodyRunID: "run", Pipeline: "build", ClaimID: "claim", SlotRunID: "run", SlotNodeID: "job", SlotGeneration: 2}
	switch credential {
	case "awarded_or_delegated_claim", "eval_unit", "here_award", "nodehost_process", "owner_process", "pod_bootstrap":
		p.Capability.Subject.Kind = capability.SubjectMachine
		p.Capability.Issuance, p.Capability.Unit, p.Capability.UnitID, p.Capability.Purpose, p.Capability.Delivery = capability.Award, capability.UnitNode, "claim", capability.PurposeWork, capability.DeliveryDirect
		p.Capability.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Generation: 2, Holder: "holder", Attempt: 1}
		if binding == "runOfTrigger" {
			p.Capability.Unit, p.Capability.Purpose = capability.UnitTrigger, capability.PurposeTrigger
			p.Capability.Binding.Kind, p.Capability.Binding.NodeID = capability.BindingTrigger, ""
			p.Capability.Binding.Attempt = 0
		}
		if binding == "planOwner" {
			p.Capability.Unit, p.Capability.Purpose = capability.UnitPlan, capability.PurposePlan
			p.Capability.Binding.Kind, p.Capability.Binding.NodeID = capability.BindingRun, ""
			p.Capability.Binding.Attempt = 0
		}
		if credential == "here_award" {
			p.Capability.Purpose = capability.PurposeHere
			p.Capability.Binding.Reservation = "here:run"
			p.Capability.Binding.Generation, p.Capability.Binding.Holder = 0, ""
		}
		if credential == "pod_bootstrap" {
			p.Capability.Delivery = capability.DeliveryBootstrap
		}
		if binding == "expansion" || binding == "sourceReport" || credential == "eval_unit" || credential == "owner_process" && binding != "runOfTrigger" && binding != "planOwner" {
			p.Capability.Unit, p.Capability.Purpose = capability.UnitEval, capability.PurposeEval
			p.Capability.Binding.Expansion = &capability.LeaseRef{Kind: capability.UnitEval, ID: "claim", Generation: 3, Holder: "eval-holder"}
			target.Unit, target.UnitID, target.UnitGeneration, target.UnitHolder, target.UnitSourceNodeID, target.UnitSourceGeneration = capability.UnitEval, "claim", 3, "eval-holder", "job", 2
		}
	case "account_session":
		p.Class = capability.CredentialSession
		p.Authority.Kind = capability.AuthoritySession
	case "verified_local_peer":
		p.Class, p.Capability.Subject.Kind = capability.CredentialPeer, capability.SubjectLocal
	case "launcher_service":
		p.Capability.Subject.Kind = capability.SubjectService
	case "deployment_admin":
		p.Capability.Subject.Kind = capability.SubjectOperator
		p.Capability.Team = ""
	case "operator_console_session":
		p.Class = capability.CredentialConsole
	case "local_run_publisher":
		p.Capability.Binding = capability.Binding{Kind: capability.BindingRun, RunID: "run"}
		p.Capability.Data = &capability.DataScope{Use: capability.DataPublish, RunID: "run", RepositoryKey: "repo"}
	case "repo_cache_reader":
		p.Capability.Data = &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "repo", VerifiedDefaultOnly: true}
	}
	if binding == "sourceBundle" {
		p.Capability.Data = &capability.DataScope{Use: capability.DataClaim, RepositoryKey: "repo", RunID: "run", NodeID: "job", Generation: 2}
		target.Data = &capability.DataTarget{Operation: capability.DataDownload, RunID: "run", NodeID: "job", Generation: 2, Digest: strings.Repeat("a", 64)}
	}
	return p, target
}
