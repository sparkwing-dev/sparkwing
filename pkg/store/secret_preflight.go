package store

import (
	"context"
	"fmt"
	"strings"
)

// safety: names every required secret the run cannot read at once, with the
// command that fixes each, and wraps [ErrPlanInvalid], so the plan is refused
// before any of its nodes exists.
type secretPreflightError struct {
	Pipeline string
	Problems []secretProblem
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

func (e *secretPreflightError) Unwrap() error { return ErrPlanInvalid }

func (e *secretPreflightError) Error() string {
	var b strings.Builder
	noun := "secret"
	if len(e.Problems) > 1 {
		noun = "secrets"
	}
	fmt.Fprintf(&b, "pipeline %q did not start: it declares %d required %s this run cannot read.", e.Pipeline, len(e.Problems), noun)
	for _, p := range e.Problems {
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
	refused := &secretPreflightError{Pipeline: pipeline}
	for _, name := range required {
		if readable[name] {
			continue
		}
		refused.Problems = append(refused.Problems, secretProblem{
			Name: name, Unshared: unshared[name], OtherPipelines: others[name],
		})
	}
	if len(refused.Problems) == 0 {
		return nil
	}
	return refused
}
