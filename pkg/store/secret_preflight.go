package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// EventKindSecretPreflightRefused records the plan a run's required secrets
// refused, with which scope each is missing from and the command that sets
// it. It is read through the run's events, which only the team reads.
const EventKindSecretPreflightRefused = "secret_preflight_refused"

// SecretPreflightError refuses a plan that declares a required secret its run
// cannot read. Its text names only those declared secrets and where a team
// member finds why, because it is answered to the planning claim; the reason
// and owners of each are in [SecretPreflightError.Detail].
type SecretPreflightError struct {
	RunID    string
	Pipeline string
	problems []secretProblem
}

type secretProblem struct {
	Name string
	// safety: a team-wide row that is not shared is told apart from no row,
	// because its fix is sharing it, not creating it.
	Unshared bool
	// safety: named so an author whose copy sits under another pipeline sees
	// where it went rather than being told it does not exist.
	OtherPipelines []string
}

func (e *SecretPreflightError) Unwrap() error { return ErrPlanInvalid }

func (e *SecretPreflightError) Error() string {
	names := make([]string, len(e.problems))
	for i, p := range e.problems {
		names[i] = p.Name
	}
	return fmt.Sprintf("pipeline %q did not start: this run cannot read %d required %s it declares: %s. "+
		"A team member sees which scope each is missing from, and the command that sets it, with: "+
		"sparkwing runs status --run %s --profile <profile>",
		e.Pipeline, len(names), plural(len(names), "secret", "secrets"), strings.Join(names, ", "), e.RunID)
}

// Detail is the refusal for the team: each secret, which scope it is missing
// from, and the command that fixes it.
func (e *SecretPreflightError) Detail() string {
	var b strings.Builder
	fmt.Fprintf(&b, "pipeline %q did not start: it declares %d required %s this run cannot read.",
		e.Pipeline, len(e.problems), plural(len(e.problems), "secret", "secrets"))
	for _, p := range e.problems {
		set := fmt.Sprintf("sparkwing secrets set --name %s --file <path> --profile <profile>", p.Name)
		switch {
		case p.Unshared:
			fmt.Fprintf(&b, "\n- %s is set team-wide but not shared with runs, so only an admin can read it."+
				"\n  Fix: %s --shared\n   or: %s --pipeline %s", p.Name, set, set, e.Pipeline)
		case len(p.OtherPipelines) > 0:
			fmt.Fprintf(&b, "\n- %s is set only for pipeline %s; pipeline %q has no copy and the team has no shared one."+
				"\n  Fix: %s --pipeline %s", p.Name, quoteList(p.OtherPipelines), e.Pipeline, set, e.Pipeline)
		default:
			fmt.Fprintf(&b, "\n- %s is not set: pipeline %q has no copy and the team has no shared one."+
				"\n  Fix: %s --pipeline %s", p.Name, e.Pipeline, set, e.Pipeline)
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

// safety: the planning claim's transaction is rolled back before this, so the
// detail lands even though the plan does not, and only once per run however
// often the claim resubmits.
func (s *Store) recordSecretPreflight(ctx context.Context, team Team, refused *SecretPreflightError) error {
	t, err := s.ForTeam(ctx, team)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"message": refused.Detail()})
	if err != nil {
		return err
	}
	_, err = t.AppendEventOnce(ctx, refused.RunID, PlanNodeID, EventKindSecretPreflightRefused, payload)
	return err
}

func quoteList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(quoted, ", ")
}

// safety: judged by the same rule the claim's release applies (the pipeline's
// own row, else a shared team-wide row), so a run that passes here is never
// refused a declared secret later unless a row changes in between.
func secretPreflightTx(ctx context.Context, tx *storeTx, team Team, runID string, required []string) (err error) {
	if len(required) == 0 {
		return nil
	}
	var pipeline string
	if err := tx.QueryRowContext(ctx, `SELECT pipeline FROM runs WHERE team = ? AND id = ?`,
		string(team), runID).Scan(&pipeline); err != nil {
		return err
	}
	args := []any{string(team)}
	for _, name := range required {
		args = append(args, name)
	}
	rows, err := tx.QueryContext(ctx, `SELECT name, pipeline, shared FROM secrets
 WHERE team = ? AND name IN (?`+strings.Repeat(", ?", len(required)-1)+`)
 ORDER BY name, pipeline`, args...)
	if err != nil {
		return err
	}
	defer closeRowsInto(rows, &err)
	readable := map[string]bool{}
	unshared := map[string]bool{}
	others := map[string][]string{}
	for rows.Next() {
		var name, owner string
		var shared int
		if err := rows.Scan(&name, &owner, &shared); err != nil {
			return err
		}
		switch {
		case owner == pipeline, owner == "" && shared != 0:
			readable[name] = true
		case owner == "":
			unshared[name] = true
		default:
			others[name] = append(others[name], owner)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	refused := &SecretPreflightError{RunID: runID, Pipeline: pipeline}
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
