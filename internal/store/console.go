package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// Console commits actions under the configured operator account gate.
type Console struct {
	s       *Store
	allowed func(string) bool
}

// AsConsole binds the live operator configuration to console transactions.
func (s *Store) AsConsole(allowed func(string) bool) *Console {
	return &Console{s: s, allowed: allowed}
}

func consoleWrite[T any](ctx context.Context, c *Console, a capability.Authorized, write func(*storeTx, auditActor) (T, error), discovery ...func(*storeTx, lockSet) lockSet) (T, error) {
	var zero T
	if c == nil || c.s == nil || c.allowed == nil || a.Principal.Class != capability.CredentialConsole || a.Principal.Capability.Subject.Kind != capability.SubjectAccount {
		return zero, authorityError(apierr.Forbidden, "direct operator session required")
	}
	var result T
	err := c.s.inTx(ctx, func(tx *storeTx) error {
		result = zero
		if err := acquireLockSet(ctx, tx, func() lockSet {
			set := discoverAuthorityLocks(ctx, tx, []capability.AuthorityRef{a.Principal.Authority})
			if a.Target.Team != "" {
				set.add(lockTeam, true, a.Target.Team)
			}
			for _, discover := range discovery {
				set = discover(tx, set)
			}
			return set
		}); err != nil {
			return err
		}
		p, err := tx.ResolveAuthority(ctx, a.Principal.Authority)
		if err != nil {
			return err
		}
		now := tx.now(ctx)
		if !capability.ConsoleAuthority(p, c.allowed(p.Capability.Subject.ID), now, 0) || p.Capability.Subject.ID != a.Principal.Capability.Subject.ID || c.s.consolePolicy != nil && !c.s.consolePolicy(p, now) {
			return authorityError(apierr.Forbidden, "direct operator session required")
		}
		result, err = write(tx, auditPrincipalActor(p))
		if err != nil {
			return err
		}
		now = tx.now(ctx)
		if !capability.ConsoleAuthority(p, c.allowed(p.Capability.Subject.ID), now, 0) || c.s.consolePolicy != nil && !c.s.consolePolicy(p, now) {
			return authorityError(apierr.Forbidden, "operator account was removed")
		}
		return nil
	})
	if err != nil {
		return zero, err
	}
	return result, nil
}

func consoleTeamTx(ctx context.Context, tx *storeTx, name string) (*Tenant, error) {
	if name == "" || string(NormalizeTeam(Team(name))) != name {
		return nil, bindingRefusal()
	}
	t := &Tenant{s: tx.store, team: Team(name)}
	if err := t.lockTeamTx(ctx, tx); err != nil {
		return nil, err
	}
	return t, nil
}

// OperatorReasonLimit is the most characters, counted as Unicode code points, an operator's reason carries.
const OperatorReasonLimit = 500

func consoleReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > OperatorReasonLimit {
		return "", authorityError(apierr.BadRequest, "reason is required, up to 500 characters")
	}
	return reason, nil
}

// ConsoleGrantCredit grants free credits once under the current operator's identity.
func (c *Console) ConsoleGrantCredit(ctx context.Context, a capability.Authorized, req CreditGrantRequest) (CreditGrantResult, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, actor auditActor) (CreditGrantResult, error) {
		t, err := consoleTeamTx(ctx, tx, a.Target.Team)
		if err != nil {
			return CreditGrantResult{}, err
		}
		if req.Kind != CreditGrantFree || !strings.HasPrefix(req.Reference, "console:") || len(req.Reference) <= len("console:") || req.Reverses != "" || req.AmountMicro <= 0 || req.AmountMicro > MaxCreditGrantMicro {
			return CreditGrantResult{}, ErrInvalidInput
		}
		req.Reason, err = consoleReason(req.Reason)
		if err != nil {
			return CreditGrantResult{}, err
		}
		req.CreatedBy = actor.subject.ID
		return recordCreditGrantTx(ctx, tx, t.team, req, actor)
	})
}

// ConsoleSetBillingTrust records trust and its audit under current operator authority.
func (c *Console) ConsoleSetBillingTrust(ctx context.Context, a capability.Authorized, change BillingTrustChange) (BillingStanding, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, actor auditActor) (BillingStanding, error) {
		t, err := consoleTeamTx(ctx, tx, a.Target.Team)
		if err != nil {
			return BillingStanding{}, err
		}
		change.Reason, err = consoleReason(change.Reason)
		if err != nil {
			return BillingStanding{}, err
		}
		change.Actor = actor.subject.ID
		_, after, err := t.setBillingTrustTx(ctx, tx, change, tx.now(ctx), actor)
		return after, err
	})
}

// ConsoleFreeze places a new operator hold and returns the committed freeze snapshot.
func (c *Console) ConsoleFreeze(ctx context.Context, a capability.Authorized, reason string) (TeamCreditFreeze, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, actor auditActor) (TeamCreditFreeze, error) {
		t, err := consoleTeamTx(ctx, tx, a.Target.Team)
		if err != nil {
			return TeamCreditFreeze{}, err
		}
		reason, err = consoleReason(reason)
		if err != nil {
			return TeamCreditFreeze{}, err
		}
		id, err := newCreditID("hold")
		if err != nil {
			return TeamCreditFreeze{}, err
		}
		if _, err := holdCreditFreezeTx(ctx, tx, t.team, operatorHoldPrefix+id, "", reason, actor.subject.ID, tx.now(ctx), actor); err != nil {
			return TeamCreditFreeze{}, err
		}
		return teamCreditFreezeTx(ctx, tx, t.team)
	})
}

// ConsoleUnfreeze releases a named hold or only operator holds when none is named.
func (c *Console) ConsoleUnfreeze(ctx context.Context, a capability.Authorized, hold, reason string, noFault bool) (TeamCreditFreeze, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, actor auditActor) (TeamCreditFreeze, error) {
		t, err := consoleTeamTx(ctx, tx, a.Target.Team)
		if err != nil {
			return TeamCreditFreeze{}, err
		}
		reason, err = consoleReason(reason)
		if err != nil {
			return TeamCreditFreeze{}, err
		}
		hold = strings.TrimSpace(hold)
		count, err := releaseCreditFreezesTx(ctx, tx, t.team, hold, hold == "", noFault, actor.subject.ID, reason, tx.now(ctx), actor)
		if err != nil {
			return TeamCreditFreeze{}, err
		}
		if count == 0 && hold != "" {
			return TeamCreditFreeze{}, ErrNotFound
		}
		return teamCreditFreezeTx(ctx, tx, t.team)
	})
}

// ConsoleDeleteTeam queues deletion after checking money in flight under current authority.
func (c *Console) ConsoleDeleteTeam(ctx context.Context, a capability.Authorized) (TeamDeletion, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, actor auditActor) (TeamDeletion, error) {
		t, err := consoleTeamTx(ctx, tx, a.Target.Team)
		if err != nil {
			return TeamDeletion{}, err
		}
		if _, err := t.markForDeletionTx(ctx, tx, actor.subject.ID, tx.now(ctx)); err != nil {
			return TeamDeletion{}, err
		}
		return teamDeletionTx(ctx, tx, t.team)
	}, func(tx *storeTx, set lockSet) lockSet {
		return discoverTeamDeletionLocks(ctx, tx, set, Team(a.Target.Team))
	})
}

// ConsoleDeleteAccount resolves the selected account and erases it in the authorized transaction.
func (c *Console) ConsoleDeleteAccount(ctx context.Context, a capability.Authorized) (AccountDeletion, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, _ auditActor) (AccountDeletion, error) {
		account, err := accountTx(ctx, tx, a.Target.AccountID)
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) {
			account, err = accountByEmailTx(ctx, tx, a.Target.AccountID)
		}
		if err != nil {
			return AccountDeletion{}, err
		}
		return tx.operator.deleteAccountTx(ctx, tx, account.ID, tx.now(ctx))
	}, func(tx *storeTx, set lockSet) lockSet {
		account, err := accountTx(ctx, tx, a.Target.AccountID)
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) {
			account, err = accountByEmailTx(ctx, tx, a.Target.AccountID)
		}
		if err != nil {
			set.err = err
			return set
		}
		return tx.operator.discoverAccountDeletionLocks(ctx, tx, set, account.ID)
	})
}

// ConsoleSetCreditSettings preserves installed price tables under current operator authority.
func (c *Console) ConsoleSetCreditSettings(ctx context.Context, a capability.Authorized, up CreditSettingsUpdate) (CreditSettings, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, _ auditActor) (CreditSettings, error) {
		if err := advisoryLockTx(ctx, tx, "sparkwing/credit-settings"); err != nil {
			return CreditSettings{}, err
		}
		return setOperatorCreditSettingsTx(ctx, tx, up)
	})
}

// ConsoleUnbindGitHubInstallation removes the current installation and its dependent schedules atomically.
func (c *Console) ConsoleUnbindGitHubInstallation(ctx context.Context, a capability.Authorized, installation int64) (Team, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, _ auditActor) (Team, error) {
		if installation <= 0 {
			return "", ErrInvalidInput
		}
		team, _, err := tx.operator.githubInstallationConnectionTx(ctx, tx, installation)
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		if err != nil {
			return "", err
		}
		t := &Tenant{s: c.s, team: team}
		return team, t.unbindGitHubAppInstallationTx(ctx, tx, installation)
	}, func(tx *storeTx, set lockSet) lockSet {
		return tx.operator.discoverInstallationLocks(ctx, tx, set, installation)
	})
}

// ConsoleApproveWaitlist admits one stable batch per request key under current operator authority.
func (c *Console) ConsoleApproveWaitlist(ctx context.Context, a capability.Authorized, ids []string, oldest int, key string) ([]Account, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, actor auditActor) ([]Account, error) {
		ids := slices.Clone(ids)
		if key == "" || oldest < 0 || (len(ids) > 0) == (oldest > 0) {
			return nil, ErrInvalidInput
		}
		request, err := json.Marshal(struct {
			IDs    []string
			Oldest int
		}{ids, oldest})
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(request)
		identity, err := json.Marshal([]string{actor.subject.ID, key})
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(identity)
		name := "console-approval:" + hex.EncodeToString(sum[:])
		var body []byte
		err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, name).Scan(&body)
		var replay [32]byte
		if err == nil {
			if err := json.Unmarshal(body, &replay); err != nil {
				return nil, err
			}
			if replay != digest {
				return nil, authorityError(apierr.Conflict, "idempotency key names another approval")
			}
			return []Account{}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if oldest > 0 {
			rows, err := tx.queryContext(ctx, `SELECT id FROM accounts WHERE waitlisted_at<>0 ORDER BY waitlisted_at,id LIMIT ?`, oldest)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return nil, err
				}
				ids = append(ids, id)
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				return nil, err
			}
		}
		replay = digest
		out := []Account{}
		for _, id := range ids {
			acct, changed, err := tx.operator.approveOneTx(ctx, tx, id, actor, tx.now(ctx))
			if err != nil {
				return nil, err
			}
			if changed {
				out = append(out, acct)
			}
		}
		body, err = json.Marshal(replay)
		if err != nil {
			return nil, err
		}
		if _, err := tx.execContext(ctx, `INSERT INTO settings(key,value) VALUES (?,?)`, name, body); err != nil {
			return nil, err
		}
		return out, nil
	})
}
