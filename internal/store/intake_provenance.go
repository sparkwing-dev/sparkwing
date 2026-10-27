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
// Retries carry their source run's sealed identity unchanged.
// An unverified run also learns the repository's verified default branch, which it
// may read verified cache records from but never writes as.
func sealProvenanceTx(ctx context.Context, tx *storeTx, team Team, request IntakeRequest, p capability.Principal, input runspec.Request, run string) (runspec.Provenance, error) {
	sealed, err := identityTx(ctx, tx, team, request, p, input, run)
	if err != nil {
		return sealed, err
	}
	if id := fetchedRepositoryTx(tx, request, input); id > 0 && sealed.Trust() == runspec.TrustAsserted && !sealed.SharedSecretsOnly() && input.Lineage.ParentRunID == "" && input.Git.Repository.Key == runspec.RepoKey("gh:"+strconv.FormatInt(id, 10)) {
		if sealed, err = refused(sealed.FetchedFrom(id)); err != nil {
			return sealed, err
		}
	}
	if sealed.Verified() || sealed.DefaultBranch() != "" {
		return sealed, nil
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

// fetchedRepositoryTx names the GitHub repository the controller itself fetches
// this run's code from through the team's App installation, at a commit a
// signed delivery named or a branch head the App resolved: a delivery's
// repository, an unlocked App-bound schedule's (fires, overrides and run-now),
// or a remote branch run the controller checked. Zero when the submitter
// supplies the code, the repository claim or an arbitrary commit.
func fetchedRepositoryTx(tx *storeTx, request IntakeRequest, input runspec.Request) int64 {
	switch {
	case input.Origin == runspec.OriginApp && request.VerifiedDeliveryID != "" && input.Git.App.Set:
		return input.Git.App.Value.RepositoryID
	case input.Origin == runspec.OriginSchedule && tx.scheduleAdmission != nil && tx.scheduleAdmission.authority() == request.Authority:
		// safety: a schedule locked to a commit runs a SHA GitHub may serve from a
		// fork, not a branch head the App resolved.
		if s := tx.scheduleAdmission.schedule; s.GitHubInstallationID > 0 && s.LockedRef == "" {
			return s.GitHubRepositoryID
		}
		return 0
	}
	return request.FetchedRepositoryID
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
		sameRepository := input.Git.Repository.Key == from.Git.Repository.Key
		if sameRepository && input.Source.Location != from.Source.Location {
			return runspec.Provenance{}, authorityError(apierr.Forbidden, "a child in its parent's repository uses its parent's source location")
		}
		if sameRepository && input.Git.Ref == from.Git.Ref && input.Git.SHA == from.Git.SHA && input.Source.Ref == from.Source.Ref {
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
		// safety: the same fire-time App lookup that proved the id reported the name, in GitHub's case.
		name := request.Request.Git.RepositoryName
		_, named := ParseGitHubRepo(name.Value)
		app := err == nil && name.Set && named && strings.HasPrefix(string(input.Git.Repository.Key), "gh:")
		if !app || !pinned || s.LockedRef != "" || s.Override != nil || !defaultBranch.Set || runspec.NormalizedBranch(s.GitBranch) != runspec.NormalizedBranch(defaultBranch.Value) {
			return asserted(runspec.RunWriter(run))
		}
		return refused(runspec.ScheduleProvenance(s.ID, repository, name.Value, defaultBranch.Value, input.Git.SHA, input.Pipeline))
	case p.Capability.Binding.Kind == capability.BindingPush && p.Capability.Binding.Push != nil:
		push := p.Capability.Binding.Push
		// safety: the ref and commit come from the push GitHub's token proved, while the
		// submitter reports the code commit, so only that same commit keeps them.
		if !strings.EqualFold(input.Source.Ref, push.SHA) {
			return asserted(runspec.RunWriter(run))
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(push.RepositoryKey, "gh:"), 10, 64)
		if err != nil || !strings.HasPrefix(push.RepositoryKey, "gh:") {
			return runspec.Provenance{}, authorityError(apierr.Forbidden, "an Actions push binding names no GitHub repository")
		}
		return refused(runspec.ActionsProvenance(id, push.Repository, push.Ref, push.SHA, input.Pipeline))
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
	armer, err := resolveTeamAuthorityTx(ctx, tx, in.schedule.Team, saved.Authority, sameTeam)
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
