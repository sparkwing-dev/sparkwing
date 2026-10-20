package store

import (
	"cmp"
	"context"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// sealProvenanceTx derives the identity a new run speaks for from facts this
// admission transaction verified, and never from what the request body claims.
// Retries never reach it: they carry their source run's sealed request whole.
func sealProvenanceTx(ctx context.Context, tx *storeTx, team Team, request IntakeRequest, p capability.Principal, input runspec.Request, run string) (runspec.Provenance, error) {
	asserted := func(writer runspec.Writer, pipeline string) (runspec.Provenance, error) {
		return refused(runspec.AssertedProvenance(writer, input.Git.Ref, input.Git.SHA, pipeline))
	}
	if input.Lineage.ParentRunID != "" {
		parent, err := readRunTx(ctx, tx, team, input.Lineage.ParentRunID)
		if err != nil {
			return runspec.Provenance{}, err
		}
		inherited := parent.Request.Provenance
		if !inherited.Valid() {
			return runspec.Provenance{}, authorityError(apierr.IntegrityFailure, "parent run has no sealed provenance")
		}
		from := parent.Request.Request
		if input.Git.Repository.Key == from.Git.Repository.Key && input.Git.Ref == from.Git.Ref && input.Git.SHA == from.Git.SHA && input.Source.Ref == from.Source.Ref {
			return inherited, nil
		}
		writer := inherited.Writer()
		if inherited.Trust() != runspec.TrustAsserted {
			writer = runspec.RunWriter(parent.ID)
		}
		return asserted(writer, inherited.Pipeline())
	}
	pinned := codePinned(input)
	switch {
	case input.Origin == runspec.OriginApp && request.VerifiedDeliveryID != "" && input.Git.App.Set:
		if !pinned {
			return asserted(runspec.RunWriter(run), input.Pipeline)
		}
		return refused(runspec.AppProvenance(input.Git.App.Value, input.PullRequest, input.Pipeline))
	case input.Origin == runspec.OriginSchedule && tx.scheduleAdmission != nil && tx.scheduleAdmission.authority() == request.Authority:
		in := tx.scheduleAdmission
		if in.caller != nil {
			subject := in.caller.Principal.Capability.Subject
			return asserted(runspec.PrincipalWriter(string(subject.Kind), subject.ID), input.Pipeline)
		}
		if !pinned || in.schedule.Override != nil && in.schedule.Override.Args != nil {
			return asserted(runspec.RunWriter(run), input.Pipeline)
		}
		return refused(runspec.ScheduleProvenance(in.schedule.ID, cmp.Or(in.schedule.ArmedBy, "github-app"), input.Git.Ref, input.Git.SHA, input.Pipeline))
	case p.Capability.Binding.Kind == capability.BindingPush && p.Capability.Binding.Push != nil:
		if !pinned {
			return asserted(runspec.RunWriter(run), input.Pipeline)
		}
		push := p.Capability.Binding.Push
		return refused(runspec.ActionsProvenance(push.Ref, push.SHA, input.Pipeline))
	}
	subject := p.Capability.Subject
	return asserted(runspec.PrincipalWriter(string(subject.Kind), subject.ID), input.Pipeline)
}

// refused reports an identity no constructor accepts as the submitter's bad request.
func refused(p runspec.Provenance, err error) (runspec.Provenance, error) {
	if err != nil {
		return runspec.Provenance{}, authorityError(apierr.BadRequest, err.Error())
	}
	return p, nil
}

// codePinned reports whether the code commit is the execution commit, or a full
// commit ID the execution commit's own configuration names. A branch or tag pin
// runs code that whoever can push that ref chooses, so it cannot keep the
// execution ref's trust.
func codePinned(input runspec.Request) bool {
	code := input.Source
	return strings.EqualFold(code.Ref, input.Git.SHA) || runspec.IsCommitID(code.RequestedRef) && strings.EqualFold(code.RequestedRef, code.Ref)
}
