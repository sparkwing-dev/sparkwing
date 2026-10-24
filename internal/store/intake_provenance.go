package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// sealProvenanceTx derives the identity a new run speaks for from facts this
// admission transaction verified, and never from what the request body claims.
// Retries carry proven identity and reseal asserted identity for their retrier.
// An unverified run also learns the repository's verified default branch, which it
// may read verified cache records from but never writes as.
func sealProvenanceTx(ctx context.Context, tx *storeTx, team Team, request IntakeRequest, p capability.Principal, input runspec.Request, run string) (runspec.Provenance, error) {
	sealed, err := identityTx(ctx, tx, team, request, p, input, run)
	if err != nil || sealed.Verified() || sealed.DefaultBranch() != "" {
		return sealed, err
	}
	var scope string
	err = tx.QueryRowContext(ctx, `SELECT ref_scope FROM blob_records WHERE team=? AND repo_key=? AND verified_default_branch=1 ORDER BY created_at DESC LIMIT 1`, team, input.Git.Repository.Key).Scan(&scope)
	if errors.Is(err, sql.ErrNoRows) {
		return sealed, nil
	}
	if err != nil {
		return runspec.Provenance{}, err
	}
	return refused(sealed.ReadingDefault(scope))
}

func identityTx(ctx context.Context, tx *storeTx, team Team, request IntakeRequest, p capability.Principal, input runspec.Request, run string) (runspec.Provenance, error) {
	asserted := func(writer runspec.Writer) (runspec.Provenance, error) {
		return refused(runspec.AssertedProvenance(writer, input.Git.Ref, input.Git.SHA, input.Pipeline))
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
		writer := runspec.RunWriter(parent.ID)
		if inherited.Trust() == runspec.TrustAsserted {
			writer = inherited.Writer()
		}
		return refused(runspec.AssertedChildProvenance(writer, input.Git.Ref, input.Git.SHA, input.Pipeline))
	}
	pinned := codePinned(input)
	switch {
	case input.Origin == runspec.OriginApp && request.VerifiedDeliveryID != "" && input.Git.App.Set:
		if !pinned {
			return asserted(runspec.RunWriter(run))
		}
		return refused(runspec.AppProvenance(input.Git.App.Value, input.PullRequest, input.Pipeline))
	case input.Origin == runspec.OriginSchedule && tx.scheduleAdmission != nil && tx.scheduleAdmission.authority() == request.Authority:
		in := tx.scheduleAdmission
		s := in.schedule
		if in.caller != nil {
			subject := in.caller.Principal.Capability.Subject
			return asserted(runspec.PrincipalWriter(string(subject.Kind), subject.ID))
		}
		admin, err := scheduleArmedByAdminTx(ctx, tx, in)
		if err != nil {
			return runspec.Provenance{}, err
		}
		if !admin {
			return asserted(runspec.PrincipalWriter("armer", s.ArmedBy))
		}
		// safety: only an unmodified fire of a following schedule, on the
		// default branch GitHub reported, whose commit the App resolved now, is verified.
		defaultBranch := request.Request.Git.DefaultBranch
		repository, err := strconv.ParseInt(strings.TrimPrefix(string(input.Git.Repository.Key), "gh:"), 10, 64)
		app := err == nil && strings.HasPrefix(string(input.Git.Repository.Key), "gh:")
		if !app || !pinned || s.LockedRef != "" || s.Override != nil || !defaultBranch.Set || runspec.NormalizedBranch(s.GitBranch) != runspec.NormalizedBranch(defaultBranch.Value) {
			return asserted(runspec.RunWriter(run))
		}
		return refused(runspec.ScheduleProvenance(s.ID, repository, defaultBranch.Value, input.Git.SHA, input.Pipeline))
	case p.Capability.Binding.Kind == capability.BindingPush && p.Capability.Binding.Push != nil:
		if !pinned {
			return asserted(runspec.RunWriter(run))
		}
		push := p.Capability.Binding.Push
		id, err := strconv.ParseInt(strings.TrimPrefix(push.RepositoryKey, "gh:"), 10, 64)
		if err != nil || !strings.HasPrefix(push.RepositoryKey, "gh:") {
			return runspec.Provenance{}, authorityError(apierr.Forbidden, "an Actions push binding names no GitHub repository")
		}
		return refused(runspec.ActionsProvenance(id, push.Ref, push.SHA, input.Pipeline))
	}
	subject := p.Capability.Subject
	return asserted(runspec.PrincipalWriter(string(subject.Kind), subject.ID))
}

// scheduleArmedByAdminTx reports whether a team admin armed the schedule: App
// automation consent, which only an admin grants, or a member who holds team.admin
// when the schedule fires.
func scheduleArmedByAdminTx(ctx context.Context, tx *storeTx, in *scheduleAdmission) (bool, error) {
	if in.consent == "" {
		return in.schedule.GitHubInstallationID > 0, nil
	}
	var saved scheduleConsent
	if err := json.Unmarshal([]byte(in.consent), &saved); err != nil {
		return false, err
	}
	armer, err := tx.ResolveAuthority(ctx, saved.Authority)
	if err != nil {
		return false, err
	}
	return slices.Contains(armer.Capability.Grants, capability.TeamAdmin), nil
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
