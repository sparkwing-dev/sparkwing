package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// LocalRunnerInstance records a local process and its persisted claim admission state.
// Machine is derived from the verified peer authority root during registration.
type LocalRunnerInstance struct {
	ID               string
	CapabilityPrefix string
	Machine          string
	PID              int
	ProcessBirth     string
	Version          string
	BuildIdentity    string
	State            string
}

// RegisterLocalRunner persists a verified local claimant without admitting claims.
// Repeating the same identity returns its current state.
func (o *Operator) RegisterLocalRunner(ctx context.Context, subject string, input LocalRunnerInstance) (LocalRunnerInstance, error) {
	if input.ID == "" || strings.TrimSpace(input.ID) != input.ID || input.PID <= 0 || strings.TrimSpace(input.ProcessBirth) == "" || strings.TrimSpace(input.Version) == "" || strings.TrimSpace(input.BuildIdentity) == "" || input.State != "" && input.State != "registered" {
		return LocalRunnerInstance{}, authorityError(apierr.BadRequest, "local runner requires an instance ID, process identity, version and build identity")
	}
	return inTxResult(ctx, o.s, func(tx *storeTx) (LocalRunnerInstance, error) {
		if err := lockClaimQueueTx(ctx, tx); err != nil {
			return LocalRunnerInstance{}, err
		}
		machine, err := localRunnerMachineTx(ctx, tx, subject, input.CapabilityPrefix)
		if err != nil {
			return LocalRunnerInstance{}, err
		}
		if input.Machine != "" && input.Machine != machine {
			return LocalRunnerInstance{}, bindingRefusal()
		}
		existing, err := readLocalRunnerTx(ctx, tx, input.ID)
		if err == nil {
			expected := input
			expected.Machine, expected.State = machine, existing.State
			if existing != expected {
				return LocalRunnerInstance{}, authorityError(apierr.Conflict, "local runner instance identity differs")
			}
			return existing, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return LocalRunnerInstance{}, err
		}
		var other string
		err = tx.QueryRowContext(ctx, `SELECT id FROM local_runner_instances WHERE capability_prefix=?`, input.CapabilityPrefix).Scan(&other)
		if err == nil {
			return LocalRunnerInstance{}, authorityError(apierr.Conflict, "local runner capability already identifies another instance")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return LocalRunnerInstance{}, err
		}
		input.Machine, input.State = machine, "registered"
		_, err = tx.execContext(ctx, `INSERT INTO local_runner_instances(id,capability_prefix,machine,pid,process_birth,version,build_identity,state) VALUES (?,?,?,?,?,?,?,?)`, input.ID, input.CapabilityPrefix, input.Machine, input.PID, input.ProcessBirth, input.Version, input.BuildIdentity, input.State)
		return input, err
	})
}

// ActivateLocalRunner atomically drains the machine's prior active instance and admits this one.
// A drained or stopped instance cannot be activated again.
func (o *Operator) ActivateLocalRunner(ctx context.Context, subject, id string) error {
	return o.changeLocalRunner(ctx, subject, id, "active")
}

// DrainLocalRunner fences new claims while preserving authority for held work.
func (o *Operator) DrainLocalRunner(ctx context.Context, subject, id string) error {
	return o.changeLocalRunner(ctx, subject, id, "draining")
}

// StopLocalRunner records that the process has joined. The caller owns joining it first.
func (o *Operator) StopLocalRunner(ctx context.Context, subject, id string) error {
	return o.changeLocalRunner(ctx, subject, id, "stopped")
}

func (o *Operator) changeLocalRunner(ctx context.Context, subject, id, state string) error {
	return o.s.inTx(ctx, func(tx *storeTx) error {
		if err := lockClaimQueueTx(ctx, tx); err != nil {
			return err
		}
		instance, err := authorizedLocalRunnerTx(ctx, tx, subject, id)
		if err != nil {
			return err
		}
		if instance.State == state || state == "draining" && instance.State == "stopped" {
			return nil
		}
		if state == "active" {
			if instance.State != "registered" {
				return authorityError(apierr.Conflict, "a drained or stopped local runner cannot activate")
			}
			if _, err := tx.execContext(ctx, `UPDATE local_runner_instances SET state='draining' WHERE machine=? AND state='active'`, instance.Machine); err != nil {
				return err
			}
		}
		_, err = tx.execContext(ctx, `UPDATE local_runner_instances SET state=? WHERE id=?`, state, id)
		return err
	})
}

// ReadLocalRunner reads one instance belonging to the verified local subject.
func (o *Operator) ReadLocalRunner(ctx context.Context, subject, id string) (_ LocalRunnerInstance, err error) {
	tx, err := o.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return LocalRunnerInstance{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	instance, err := authorizedLocalRunnerTx(ctx, tx, subject, id)
	if err != nil {
		return LocalRunnerInstance{}, err
	}
	return instance, nil
}

// ActiveLocalRunner finds the current instance for the verified local subject.
// Absence, including an active instance whose claim credential has ended, is
// ErrNotFound; a stored identity is not proof that its process is alive.
func (o *Operator) ActiveLocalRunner(ctx context.Context, subject string) (_ LocalRunnerInstance, err error) {
	tx, err := o.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return LocalRunnerInstance{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	rows, err := tx.queryContext(ctx, `SELECT prefix FROM capabilities WHERE team=? AND subject_kind='local' AND subject_id=? AND parent_id IS NULL ORDER BY created_at,prefix`, DefaultTeam, subject)
	if err != nil {
		return LocalRunnerInstance{}, err
	}
	prefixes, err := scanPrefixes(rows)
	if err != nil {
		return LocalRunnerInstance{}, err
	}
	var machine string
	for _, prefix := range prefixes {
		record, err := tx.operator.loadCapabilityRecordTx(ctx, tx, prefix)
		if err != nil {
			return LocalRunnerInstance{}, err
		}
		if record.Value.CredentialEpoch == o.s.credentialEpoch {
			machine = prefix
			break
		}
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM local_runner_instances WHERE machine=? AND state='active'`, machine).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return LocalRunnerInstance{}, notFound("active local runner", "")
	}
	if err != nil {
		return LocalRunnerInstance{}, err
	}
	instance, err := authorizedLocalRunnerTx(ctx, tx, subject, id)
	// safety: a runner whose claim credential ended can never claim again; reporting none lets the
	// supervisor start a replacement, whose activation drains this row.
	if unavailableRunner(err) {
		return LocalRunnerInstance{}, notFound("active local runner", "")
	}
	return instance, err
}

func authorizedLocalRunnerTx(ctx context.Context, tx *storeTx, subject, id string) (LocalRunnerInstance, error) {
	instance, err := readLocalRunnerTx(ctx, tx, id)
	if err != nil {
		return LocalRunnerInstance{}, err
	}
	machine, err := localRunnerMachineTx(ctx, tx, subject, instance.CapabilityPrefix)
	if err != nil {
		return LocalRunnerInstance{}, err
	}
	if instance.Machine != machine {
		return LocalRunnerInstance{}, bindingRefusal()
	}
	return instance, nil
}

func readLocalRunnerTx(ctx context.Context, tx *storeTx, id string) (LocalRunnerInstance, error) {
	var out LocalRunnerInstance
	err := tx.QueryRowContext(ctx, `SELECT id,capability_prefix,machine,pid,process_birth,version,build_identity,state FROM local_runner_instances WHERE id=?`, id).Scan(&out.ID, &out.CapabilityPrefix, &out.Machine, &out.PID, &out.ProcessBirth, &out.Version, &out.BuildIdentity, &out.State)
	if errors.Is(err, sql.ErrNoRows) {
		return LocalRunnerInstance{}, notFound("local runner", id)
	}
	return out, err
}

func localRunnerMachineTx(ctx context.Context, tx *storeTx, subject, prefix string) (string, error) {
	chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}, true)
	if err != nil {
		return "", err
	}
	leaf, root := chain[0].principal.Capability, chain[len(chain)-1].principal.Capability
	if len(chain) < 2 || subject == "" || !localPeerRoot(root) || root.Subject.ID != subject || leaf.Subject != root.Subject || leaf.Team != string(DefaultTeam) || leaf.Audience != capability.Controller || leaf.Binding != (capability.Binding{Kind: capability.BindingNone}) || leaf.Unit != "" || leaf.UnitID != "" || leaf.Purpose != "" || len(leaf.Grants) != 2 || !slices.Contains(leaf.Grants, capability.NodesClaim) || !slices.Contains(leaf.Grants, capability.TriggersClaim) {
		return "", bindingRefusal()
	}
	return root.Prefix, nil
}

func activeLocalRunnerTx(ctx context.Context, tx *storeTx, prefix string) error {
	check := func(candidate string) (bool, error) {
		state, err := localRunnerStateTx(ctx, tx, candidate)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if state != "active" {
			return true, notFound("eligible work", "")
		}
		return true, nil
	}
	// perf: registered runner polls need only the exact lookup; held owner awards require ancestry.
	if found, err := check(prefix); found || err != nil {
		return err
	}
	chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}, false)
	if err != nil {
		return err
	}
	for _, ancestor := range chain[1:] {
		if found, err := check(ancestor.principal.Capability.Prefix); found || err != nil {
			return err
		}
	}
	return nil
}

func localRunnerStateTx(ctx context.Context, tx *storeTx, prefix string) (string, error) {
	var state string
	err := tx.QueryRowContext(ctx, `SELECT state FROM local_runner_instances WHERE capability_prefix=?`, prefix).Scan(&state)
	return state, err
}
