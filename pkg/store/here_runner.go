package store

import (
	"context"
	"strings"
	"time"
)

// HereRunnerPrincipalPrefix begins the principal of every credential minted
// for a `pipeline trigger --runner here` run; the run id follows it. The
// controller confines such a credential to that run, and only
// [Tenant.CreateHereTriggerWithRun] mints one.
const HereRunnerPrincipalPrefix = "here:"

// HereRunnerClaimWindow is how long after its trigger is written a here-runner
// credential may claim it. Past it the pinned trigger is claimable by no one.
const HereRunnerClaimWindow = 10 * time.Minute

// HereRunner describes the one-shot runner a trigger is pinned to.
type HereRunner struct {
	// Reason is the caller's recorded justification, kept on the run's
	// runner_here event.
	Reason string
	// By names the caller that triggered the run.
	By string
	// CreatedBy is the caller's account id, which lets that account revoke
	// the credential from team settings.
	CreatedBy string
	Scopes    []string
	TTL       time.Duration
}

// HereRunnerRunID reads the run a here-runner principal is bound to.
func HereRunnerRunID(principal string) (runID string, isHere bool) {
	runID, isHere = strings.CutPrefix(principal, HereRunnerPrincipalPrefix)
	return runID, isHere
}

// CreateHereTriggerWithRun writes trig and its pending run pinned to a runner
// credential minted in the same transaction, and returns that credential.
//
// safety: the pin is the credential's prefix, so only that credential may
// claim the trigger, only while it has never been claimed, and no other
// runner sees it in the queue. The credential, the pin and the runner_here
// event commit together, so no run exists whose here-runner is unaudited.
func (t *Tenant) CreateHereTriggerWithRun(ctx context.Context, trig Trigger, r Run, here HereRunner, now time.Time) (string, *Token, error) {
	if here.TTL <= 0 || strings.TrimSpace(here.Reason) == "" || trig.ID == "" || trig.ID != r.ID {
		return "", nil, ErrInvalidInput
	}
	if err := refuseAdminScope(here.Scopes); err != nil {
		return "", nil, err
	}
	for attempt := 1; ; attempt++ {
		raw, tok, err := t.createHereTriggerOnce(ctx, trig, r, here, now)
		if err == nil {
			return raw, tok, nil
		}
		if attempt < mintAttempts && isTokenPrefixCollision(err) {
			continue
		}
		return "", nil, err
	}
}

func (t *Tenant) createHereTriggerOnce(ctx context.Context, trig Trigger, r Run, here HereRunner, now time.Time) (_ string, _ *Token, err error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return "", nil, err
	}
	defer rollbackUnlessDone(tx, &err)
	at := now.UTC().Unix()
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM tokens
		WHERE team = ? AND kind = ? AND principal LIKE ? AND expires_at IS NOT NULL AND expires_at <= ?`,
		string(t.team), TokenKindRunner, HereRunnerPrincipalPrefix+"%", at); err != nil {
		return "", nil, err
	}
	raw, tok, err := createTokenRow(ctx, tx, t.team, HereRunnerPrincipalPrefix+trig.ID, TokenKindRunner,
		here.Scopes, here.TTL, now, TokenOptions{CreatedBy: here.CreatedBy})
	if err != nil {
		return "", nil, err
	}
	trig.RunnerPin = tok.Prefix
	if err := createTriggerTx(ctx, tx, t.team, trig); err != nil {
		return "", nil, err
	}
	if err := t.s.createRunTx(ctx, tx, t.team, r); err != nil {
		return "", nil, err
	}
	if _, err := appendEventTx(ctx, tx, t.team, trig.ID, "", "runner_here", map[string]any{
		"reason": here.Reason, "by": here.By, "token_prefix": tok.Prefix, "expires_at": tok.ExpiresAt.UTC().Unix(),
	}, now); err != nil {
		return "", nil, err
	}
	if err := tx.Commit(); err != nil {
		return "", nil, err
	}
	return raw, tok, nil
}
