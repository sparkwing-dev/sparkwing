package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// ValidateRunnerLabels bounds enrollment and claim metadata before it is stored or shown for approval.
// Labels are trimmed like matching input and may contain ASCII letters, digits, '-', '_', '.', ':', '/' and '='.
func ValidateRunnerLabels(labels []string) error {
	if len(labels) > 32 {
		return authorityError(apierr.BadRequest, "runner labels must contain at most 32 entries; remove extra labels")
	}
	for i, raw := range labels {
		label := strings.TrimSpace(raw)
		if len(label) == 0 || len(label) > 128 {
			return authorityError(apierr.BadRequest, fmt.Sprintf("runner labels entry %d must be 1 to 128 characters after trimming whitespace", i+1))
		}
		for _, c := range label {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.:/=", c) {
				continue
			}
			return authorityError(apierr.BadRequest, fmt.Sprintf("runner labels entry %d may contain only ASCII letters, digits, '-', '_', '.', ':', '/' and '='; remove spaces, control characters and selector punctuation", i+1))
		}
	}
	return nil
}

func checkRunnerLabelsTx(ctx context.Context, tx *storeTx, chain []authorityNode, labels []string) error {
	if err := ValidateRunnerLabels(labels); err != nil {
		return err
	}
	for _, node := range chain {
		if node.principal.Capability.Purpose == capability.PurposeHere {
			return nil
		}
	}
	root := chain[len(chain)-1].principal
	if !registeredClaimant(root.Capability) {
		return nil
	}
	record, err := tx.operator.loadCapabilityRecordTx(ctx, tx, root.Authority.ID)
	if err != nil {
		return err
	}
	if record.Origin == nil || record.Origin.Kind != capability.ProvisionMachine {
		return nil
	}
	for _, label := range match.SelfAsserted(labels) {
		if !slices.Contains(record.Origin.AllowedLabels, label) {
			return authorityError(apierr.Forbidden, fmt.Sprintf("runner %q may not advertise label %q; ask a team administrator to revoke this enrollment and re-enroll with sparkwing runners add --runner %s --label LABEL --force (repeat --label for the complete allowed set and retain the other enrollment settings). Self-hosted operators can instead update allowed labels with sparkwing-controller admin runner-labels --operator ACCOUNT_ID --team %s --runner %s --reason TEXT --label LABEL", root.Capability.Subject.ID, label, root.Capability.Subject.ID, root.Capability.Team, root.Capability.Subject.ID))
		}
	}
	return nil
}

// ConfigureRunnerLabels reads or replaces an enrolled runner's allowed labels using in-pod administration.
// A nil replacement reads the set; an empty replacement clears it. Operator must be a team administrator.
func (o *Operator) ConfigureRunnerLabels(ctx context.Context, team Team, name, operator string, replacement *[]string) ([]string, error) {
	if replacement != nil {
		if err := ValidateRunnerLabels(*replacement); err != nil {
			return nil, err
		}
	}
	return inTxResult(ctx, o.s, func(tx *storeTx) ([]string, error) {
		if err := acquireLockSet(ctx, tx, func() lockSet {
			set := tx.operator.discoverAccountTeams(ctx, tx, lockSet{}, operator)
			set.add(lockTeam, true, string(team))
			return set
		}); err != nil {
			return nil, err
		}
		var role Role
		err := tx.QueryRowContext(ctx, `SELECT role FROM memberships WHERE team=? AND account_id=?`, team, operator).Scan(&role)
		if err != nil || role != RoleOwner {
			return nil, authorityError(apierr.Forbidden, "runner labels require an existing team administrator in --team")
		}
		now := tx.now(ctx).UnixNano()
		rows, err := tx.queryContext(ctx, `SELECT prefix FROM capabilities WHERE team=? AND subject_kind='machine' AND subject_id=? AND parent_id IS NULL AND bind_kind='none' AND `+liveCredentialSQL("")+` ORDER BY created_at,prefix`, team, name, now, now)
		if err != nil {
			return nil, err
		}
		prefixes, err := scanPrefixes(rows)
		if err != nil {
			return nil, err
		}
		if len(prefixes) == 0 {
			return nil, ErrNotFound
		}
		refs := make([]capability.AuthorityRef, len(prefixes))
		for i, prefix := range prefixes {
			refs[i] = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}
		}
		if err := o.lockAuthorityTx(ctx, tx, refs, prefixes...); err != nil {
			return nil, err
		}
		var labels []string
		for _, prefix := range prefixes {
			record, err := o.loadCapabilityRecordTx(ctx, tx, prefix)
			if err != nil {
				return nil, err
			}
			if record.Origin == nil || record.Origin.Kind != capability.ProvisionMachine {
				return nil, bindingRefusal()
			}
			labels = slices.Clone(record.Origin.AllowedLabels)
			if replacement != nil {
				labels = match.SelfAsserted(*replacement)
				record.Origin.AllowedLabels = labels
				body, err := json.Marshal(record)
				if err != nil {
					return nil, err
				}
				if _, err := tx.execContext(ctx, `UPDATE capabilities SET authority_json=? WHERE prefix=? AND team=?`, body, prefix, team); err != nil {
					return nil, err
				}
			}
		}
		if replacement != nil {
			if err := recordMutationAuditTx(ctx, tx, team, auditActor{subject: capability.Subject{Kind: capability.SubjectAccount, ID: operator}}, "runner.labels", name); err != nil {
				return nil, err
			}
		}
		return labels, nil
	})
}

// safety: freeze only labels recorded before upgrade, never a new claimant's first request.
func migrateRunnerLabelsTx(ctx context.Context, tx *storeTx) error {
	rows, err := tx.queryContext(ctx, `SELECT prefix FROM capabilities WHERE subject_kind='machine' AND parent_id IS NULL AND bind_kind='none'`)
	if err != nil {
		return err
	}
	prefixes, err := scanPrefixes(rows)
	if err != nil {
		return err
	}
	for _, prefix := range prefixes {
		record, err := tx.operator.loadCapabilityRecordTx(ctx, tx, prefix)
		if err != nil {
			return err
		}
		if record.Origin == nil || record.Origin.Kind != capability.ProvisionMachine || record.Origin.AllowedLabels != nil {
			continue
		}
		var advertised []byte
		if err := tx.QueryRowContext(ctx, `SELECT advertised_labels_json FROM capabilities WHERE prefix=?`, prefix).Scan(&advertised); err != nil {
			return err
		}
		var labels []string
		if len(advertised) > 0 {
			if err := json.Unmarshal(advertised, &labels); err != nil {
				return err
			}
		}
		record.Origin.AllowedLabels = match.SelfAsserted(labels)
		body, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if _, err := tx.execContext(ctx, `UPDATE capabilities SET authority_json=? WHERE prefix=?`, body, prefix); err != nil {
			return err
		}
	}
	return nil
}
