package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// secretPreflightError refuses a plan that declares a required secret its run
// cannot read. Its text names only those declared secrets and where a team
// member finds why, because it is answered to the planning claim; the reason
// and owners of each are in [secretPreflightError.detail].
type secretPreflightError struct {
	RunID      string
	Pipeline   string
	Repo       string
	problems   []secretProblem
	sharedOnly bool
	local      bool
}

type secretProblem struct {
	Name string
	// safety: a team-wide row that is not shared is told apart from no row,
	// because its fix is sharing it, not creating it.
	Unshared bool
	// safety: named so an author whose copy sits under another repository or
	// pipeline sees where it went rather than being told it does not exist.
	OtherPipelines []string
}

func (e *secretPreflightError) Unwrap() error { return authorityError(apierr.PlanInvalid, e.Error()) }

func (e *secretPreflightError) Error() string {
	if e.local {
		return e.detail()
	}
	names := make([]string, len(e.problems))
	for i, p := range e.problems {
		names[i] = p.Name
	}
	return fmt.Sprintf("pipeline %q did not start: this run cannot read %d required %s it declares: %s. "+
		"A team member sees which scope each is missing from, and the command that sets it, with: "+
		"sparkwing runs status --run %s --profile <profile>",
		e.Pipeline, len(names), plural(len(names), "secret", "secrets"), strings.Join(names, ", "), e.RunID)
}

// detail is the refusal for the team: each secret, which scope it is missing
// from, and the command that fixes it.
func (e *secretPreflightError) detail() string {
	var b strings.Builder
	fmt.Fprintf(&b, "pipeline %q did not start: it declares %d required %s this run cannot read.",
		e.Pipeline, len(e.problems), plural(len(e.problems), "secret", "secrets"))
	for _, p := range e.problems {
		if e.local {
			fmt.Fprintf(&b, "\n- %s is unavailable in the local controller.\n  Fix: sparkwing secrets set --secret %s --file <path>", p.Name, p.Name)
			continue
		}
		set := fmt.Sprintf("sparkwing secrets set --secret %s --file <path> --profile <profile>", p.Name)
		scoped := fmt.Sprintf("%s --repo %s --pipeline %s", set, e.Repo, e.Pipeline)
		switch {
		case e.sharedOnly:
			fmt.Fprintf(&b, "\n- %s is unavailable: this run's code did not come from a repository Sparkwing fetched (an uploaded, local, here or changed-revision child run), so it reads only shared team-wide secrets."+
				"\n  Fix: %s --shared\n   or: run it from the connected GitHub repository instead of an upload", p.Name, set)
		case p.Unshared:
			fmt.Fprintf(&b, "\n- %s is set team-wide but not shared with runs, so only an admin can read it."+
				"\n  Fix: %s --shared\n   or: %s", p.Name, set, scoped)
		case len(p.OtherPipelines) > 0:
			fmt.Fprintf(&b, "\n- %s is set only for %s; pipeline %q of %s has no copy and the team has no shared one."+
				"\n  Fix: %s", p.Name, strings.Join(p.OtherPipelines, ", "), e.Pipeline, e.Repo, scoped)
		default:
			fmt.Fprintf(&b, "\n- %s is not set: pipeline %q of %s has no copy and the team has no shared one."+
				"\n  Fix: %s", p.Name, e.Pipeline, e.Repo, scoped)
		}
	}
	b.WriteString("\nIf the run can do without a secret, declare it optional: tag its field in Secrets() `sw:\"NAME,optional\"`.")
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func (s *Store) recordSecretPreflight(ctx context.Context, team Team, request PlanAcceptance, refused *secretPreflightError) error {
	return s.inTx(ctx, func(tx *storeTx) error {
		if err := planAuthorityTx(ctx, tx, team, request); err != nil {
			return err
		}
		if err := lockStateRunTx(ctx, tx, team, refused.RunID); err != nil {
			return err
		}
		var existing int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE team=? AND run_id=? AND node_id=? AND kind=?`, team, refused.RunID, PlanNodeID, runstate.EventSecretPreflightRefused).Scan(&existing); err != nil || existing != 0 {
			return err
		}
		if _, err := appendEventTx(ctx, tx, team, refused.RunID, PlanNodeID, runstate.EventSecretPreflightRefused, map[string]string{"message": refused.detail()}, tx.now(ctx), true); err != nil {
			return err
		}
		return planAuthorityTx(ctx, tx, team, request)
	})
}

// The release selector also owns preflight eligibility so an unchanged row
// cannot pass planning and fail release.
func secretPreflightTx(ctx context.Context, tx *storeTx, team Team, run RunRow, selection secretSelection, required []string) (err error) {
	if run.Request.Request.Mode == runspec.IntakePublish {
		return nil
	}
	if len(required) == 0 {
		return nil
	}
	runID := run.ID
	readable := map[string]bool{}
	for _, name := range run.Request.Request.LocalSecretNames {
		if _, err := tx.store.localSecret(team, runID, name); err != nil {
			return err
		}
		readable[name] = true
	}
	selected, err := selectedSecretsTx(ctx, tx, team, "", selection)
	if err != nil {
		return err
	}
	for _, row := range selected {
		readable[row.secret.Name] = true
	}
	args := []any{string(team)}
	for _, name := range required {
		args = append(args, name)
	}
	rows, err := tx.queryContext(ctx, `SELECT name, repo_name, pipeline, shared FROM secrets
 WHERE team = ? AND name IN (?`+strings.Repeat(", ?", len(required)-1)+`)
 ORDER BY name, repo_name, pipeline`, args...)
	if err != nil {
		return err
	}
	defer closeRowsInto(rows, &err)
	unshared := map[string]bool{}
	others := map[string][]string{}
	for rows.Next() {
		var name, repo, owner string
		var shared int
		if err := rows.Scan(&name, &repo, &owner, &shared); err != nil {
			return err
		}
		switch {
		case owner == "" && shared == 0:
			unshared[name] = true
		case owner != "":
			others[name] = append(others[name], fmt.Sprintf("pipeline %q of %s", owner, repo))
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	pipeline := selection.pipeline
	if pipeline == "" {
		pipeline = run.Request.Provenance.Pipeline()
	}
	repo := run.Request.Provenance.Repository()
	if repo == "" {
		repo = selection.repo
	}
	local := run.Request.Request.Origin == runspec.OriginLocal || run.Request.Request.Source.Location.LocalCheckout != ""
	refused := &secretPreflightError{RunID: runID, Pipeline: pipeline, Repo: repo, sharedOnly: selection.scope == secretSharedOnly, local: local}
	for _, name := range required {
		if readable[name] {
			continue
		}
		refused.problems = append(refused.problems, secretProblem{
			Name: name, Unshared: unshared[name], OtherPipelines: others[name],
		})
	}
	if len(refused.problems) == 0 {
		return nil
	}
	return refused
}
