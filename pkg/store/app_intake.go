package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// GitHubAppAdmission carries provider-verified delivery facts from the trusted webhook adapter.
type GitHubAppAdmission struct {
	Request      runspec.Request
	Installation GitHubAppInstallation
	Subscription *GitHubAppTrigger
	Automation   *GitHubAppAutomation
	Event        string
}

func (in GitHubAppAdmission) authority() capability.AuthorityRef {
	return capability.AuthorityRef{Kind: "github_app", ID: fmt.Sprintf("%d:%d", in.Installation.InstallationID, in.Request.Git.App.Value.RepositoryID)}
}

// AdmitGitHubAppRun rechecks the installation and consent inside the shared sealed intake transaction.
func (t *Tenant) AdmitGitHubAppRun(ctx context.Context, in GitHubAppAdmission) (AdmissionResult, error) {
	app := in.Request.Git.App
	if !app.Set || app.Value.VerifiedAt.IsZero() || app.Value.DeliveryKey == "" || app.Value.RepositoryID <= 0 || app.Value.InstallationID != in.Installation.InstallationID || in.Installation.Team != t.team || in.Installation.CreatedAt.IsZero() ||
		string(in.Request.Git.Repository.Key) != fmt.Sprintf("gh:%d", app.Value.RepositoryID) || app.Value.SHA != in.Request.Git.SHA || app.Value.Ref != in.Request.Git.Ref || in.Request.Origin != runspec.OriginApp || in.Request.Mode != runspec.IntakeExecute || (in.Subscription == nil) == (in.Automation == nil) {
		return AdmissionResult{}, ErrInvalidInput
	}
	var result AdmissionResult
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		tx.appAdmission = &in
		var err error
		result, err = admitRun(ctx, tx, t.team, IntakeRequest{Authority: in.authority(), Request: in.Request, VerifiedDeliveryID: app.Value.DeliveryKey}, tx.now())
		return err
	})
	if err != nil {
		return AdmissionResult{}, err
	}
	return result, nil
}

func (in GitHubAppAdmission) resolve(ctx context.Context, tx *storeTx) (capability.Principal, error) {
	refuse := func() (capability.Principal, error) { return capability.Principal{}, ErrGitHubAppAutomationRevoked }
	var created int64
	err := tx.QueryRowContext(ctx, `SELECT created_at FROM github_app_installations WHERE team=? AND installation_id=? AND suspended=0`+tx.forUpdate(), in.Installation.Team, in.Installation.InstallationID).Scan(&created)
	if errors.Is(err, sql.ErrNoRows) || err == nil && created != in.Installation.CreatedAt.UnixNano() {
		return refuse()
	}
	if err != nil {
		return capability.Principal{}, err
	}
	app := in.Request.Git.App.Value
	var event string
	err = tx.QueryRowContext(ctx, `SELECT event FROM github_app_deliveries WHERE digest=?`+tx.forShare(), "event:"+app.DeliveryKey).Scan(&event)
	if errors.Is(err, sql.ErrNoRows) || err == nil && event != in.Event {
		return refuse()
	}
	if err != nil {
		return capability.Principal{}, err
	}
	if in.Automation != nil {
		grant := *in.Automation
		if grant.InstallationID != app.InstallationID || grant.RepositoryID != app.RepositoryID {
			return refuse()
		}
		if err := (&Tenant{s: tx.store, team: in.Installation.Team}).checkGitHubAppAutomationTx(ctx, tx, grant); err != nil {
			return capability.Principal{}, err
		}
	} else {
		sub := *in.Subscription
		if sub.InstallationID != app.InstallationID || sub.RepositoryID != app.RepositoryID || sub.Pipeline != in.Request.Pipeline {
			return refuse()
		}
		var generation int64
		err := tx.QueryRowContext(ctx, `SELECT created_at FROM github_app_triggers WHERE team=? AND installation_id=? AND repository_id=? AND pipeline=?`+tx.forShare(), in.Installation.Team, app.InstallationID, app.RepositoryID, sub.Pipeline).Scan(&generation)
		if errors.Is(err, sql.ErrNoRows) || err == nil && generation != sub.CreatedAt.UnixNano() {
			return refuse()
		}
		if err != nil {
			return capability.Principal{}, err
		}
	}
	bound, err := tx.ExecContext(ctx, `UPDATE github_app_deliveries SET admitted_team=? WHERE digest=? AND (admitted_team='' OR admitted_team=?)`, in.Installation.Team, "event:"+app.DeliveryKey, in.Installation.Team)
	if err != nil {
		return capability.Principal{}, err
	}
	if n, err := bound.RowsAffected(); err != nil {
		return capability.Principal{}, err
	} else if n != 1 {
		return refuse()
	}
	ref := in.authority()
	return capability.Principal{Authority: ref, Capability: capability.Capability{Team: string(in.Installation.Team), Audience: capability.Controller, Subject: capability.Subject{Kind: capability.SubjectService, ID: ref.ID}, Grants: []capability.Grant{capability.RunsWrite}, Binding: capability.Binding{Kind: capability.BindingNone}}}, nil
}

// VerifiedAppIntake returns facts only from the trusted origin attached to this admission transaction.
func VerifiedAppIntake(ctx context.Context, resolver capability.AuthorityResolver, request IntakeRequest) (VerifiedRunDelivery, error) {
	tx, ok := resolver.(*storeTx)
	if !ok || tx.appAdmission == nil || tx.appAdmission.authority() != request.Authority || tx.appAdmission.Request.Git.App.Value.DeliveryKey != request.VerifiedDeliveryID {
		return VerifiedRunDelivery{}, authorityError(apierr.Forbidden, "verified App origin is unavailable")
	}
	if _, err := tx.appAdmission.resolve(ctx, tx); err != nil {
		return VerifiedRunDelivery{}, err
	}
	in := tx.appAdmission.Request
	return VerifiedRunDelivery{Git: in.Git, Actor: in.Actor, PullRequest: in.PullRequest}, nil
}
