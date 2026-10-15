package main

import (
	"bytes"
	"encoding/json"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func ownerRunInput(run string, award controllerwire.ClaimedUnit, purpose capability.AwardPurpose) (nodehost.RunInput, error) {
	input := nodehost.RunInput{RunID: run, RequestHash: string(award.Request.Hash), GrantedRunner: award.Runner}
	input.GrantedRunner.Labels = slices.Clone(award.Runner.Labels)
	if run == "" || award.Unit != capability.UnitTrigger || award.Purpose != purpose || award.Delivery != capability.DeliveryDirect || award.RunID != run || award.UnitID == "" || award.JobID != "" || award.Capability == "" || award.Binding.Kind != capability.BindingTrigger || award.Binding.RunID != run || award.Binding.NodeID != "" || award.Binding.Holder == "" || award.Binding.Generation <= 0 || award.LeaseExpiresAt.IsZero() {
		return input, localRunProtocolError("controller returned an invalid owner award")
	}
	input.Owner = store.Actor{Kind: store.ActorRunHost, Owner: store.OwnerLeaseRef{
		Kind: store.OwnerTrigger, RunID: run,
		Holder: award.Binding.Holder, Generation: award.Binding.Generation,
	}}
	sealed := award.Request
	hash, err := runspec.CanonicalHash(sealed)
	if err != nil || sealed.RunID == "" || sealed.Version != 1 || sealed.Request.Version != 1 || sealed.Hash == "" || hash != sealed.Hash {
		return input, localRunProtocolError("controller returned an invalid owner request seal")
	}
	input.SealedRequest, err = json.Marshal(sealed.Request)
	if err != nil || award.Accepted == nil {
		return input, err
	}
	accepted := award.Accepted
	_, sealErr := runspec.CheckSeal(accepted.Spec, accepted.Spec)
	planHash, planErr := runspec.CanonicalHash(accepted.Plan)
	if sealErr != nil || planErr != nil || planHash != accepted.PlanHash || accepted.Spec.Request.Hash != sealed.Hash || accepted.Spec.PlanHash != accepted.PlanHash {
		return input, localRunProtocolError("controller returned inconsistent accepted owner seals")
	}
	body, err := json.Marshal(accepted.Spec)
	if err != nil {
		return input, err
	}
	input.Accepted = &nodehost.AcceptedRun{RunID: run, Spec: body, SpecHash: string(accepted.Spec.Hash), PlanHash: string(accepted.PlanHash)}
	decoder := json.NewDecoder(bytes.NewReader(accepted.Plan))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input.Accepted.Plan); err != nil {
		return input, localRunProtocolError("controller returned an invalid accepted owner plan")
	}
	return input, nil
}
