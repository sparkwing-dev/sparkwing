package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// TestOnlySeedRun inserts reporting facts without admitting executable work.
func (t *Tenant) TestOnlySeedRun(ctx context.Context, r Run) error {
	if r.DeclaredRepo == "" {
		r.DeclaredRepo = r.RepoURL
	}
	now := r.CreatedAt
	if now.IsZero() {
		now = r.StartedAt
		if now.IsZero() {
			now = time.Now().UTC()
		}
	}
	request := runspec.SealedRequest{Version: 1, RunID: r.ID, AdmittedAt: now, Request: runspec.Request{
		Version: 1, Team: string(t.team), Pipeline: r.Pipeline, Mode: runspec.IntakeExecute,
		Dispatch: runspec.DispatchHost, RequestedArgs: r.Args,
		Git:     runspec.Git{Repository: runspec.Repository{Key: runspec.RepoKey(r.DeclaredRepo), Display: r.DeclaredRepo}, Ref: r.GitBranch, SHA: r.GitSHA},
		Source:  runspec.Source{Location: runspec.SourceLocation{RemoteURL: r.RepoURL}},
		Lineage: runspec.Lineage{ParentRunID: r.ParentRunID, RetryOf: r.RetryOf},
	}}
	if request.Request.Git.Repository.Key == "" {
		request.Request.Git.Repository.Key = "gh:1"
	}
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	var start any
	if !r.StartedAt.IsZero() {
		start = r.StartedAt.UnixNano()
	}
	finish := nullableTimeNS(r.FinishedAt)
	if isTerminalRunStatus(r.Status) && finish == nil {
		finish = now.UnixNano()
	}
	return t.s.inTx(ctx, func(tx *storeTx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,allowed_pools,created_principal,created_at,started_at,finished_at,trigger_source,git_branch,git_sha,parent_run_id,retry_of,replay_of_run_id,replay_of_node_id,log_retention_class,log_retention_days)
SELECT ?,?,COALESCE(MAX(number),0)+1,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'r30',30 FROM runs WHERE team=?
ON CONFLICT(id) DO NOTHING`, t.team, r.ID, r.Pipeline, r.Status, body, "fixture", now.UnixNano(), request.Request.Git.Repository.Key, r.DeclaredRepo, "host", "execute", []byte(`[]`), creatingPrincipal(ctx), now.UnixNano(), start, finish, r.TriggerSource, r.GitBranch, r.GitSHA, sql.NullString{String: r.ParentRunID, Valid: r.ParentRunID != ""}, r.RetryOf, r.ReplayOfRunID, r.ReplayOfNodeID, t.team)
		return err
	})
}

// TestOnlySeedTrigger inserts an owner row for reporting and maintenance fixtures.
func (t *Tenant) TestOnlySeedTrigger(ctx context.Context, v Trigger) error {
	if v.Repo == "" {
		v.Repo = v.TriggerEnv["GITHUB_REPOSITORY"]
	}
	status := v.Status
	if status == "" {
		status = "pending"
	}
	now := v.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := t.TestOnlySeedRun(ctx, Run{ID: v.ID, Pipeline: v.Pipeline, Status: "pending", CreatedAt: now, Args: v.Args, DeclaredRepo: v.Repo, RepoURL: v.RepoURL, GitBranch: v.GitBranch, GitSHA: v.GitSHA, TriggerSource: v.TriggerSource, ParentRunID: v.ParentRunID, RetryOf: v.RetryOf}); err != nil {
		return err
	}
	return t.s.inTx(ctx, func(tx *storeTx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,needs_labels,prefers_labels,webhook_delivery,webhook_replay_key) VALUES (?,?,?,'trigger',?,?,?,?,?,?)`, t.team, v.ID, status, now.UnixNano(), now.UnixNano(), []byte(`[]`), []byte(`[]`), v.WebhookDelivery, v.WebhookReplayKey)
		return err
	})
}

// TestOnlySettlePaidCredit funds a fixture through checkout settlement with trusted provider facts.
func (t *Tenant) TestOnlySettlePaidCredit(ctx context.Context, req CreditGrantRequest) (CreditGrantResult, error) {
	if req.Kind != CreditGrantPaid || req.AmountMicro <= 0 || req.AmountMicro%MicroCreditsPerCent != 0 || req.Reference == "" {
		return CreditGrantResult{}, ErrInvalidInput
	}
	now := t.s.now()
	checkout := "fixture-checkout:" + req.Reference
	if err := t.s.inTx(ctx, func(tx *storeTx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES ('payment_mode',?) ON CONFLICT(key) DO NOTHING`, []byte(`"test"`)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at) VALUES (?,?,?,'test','usd',?,?,?,?) ON CONFLICT(id) DO NOTHING`, "fixture-row:"+req.Reference, t.team, checkout, req.AmountMicro/MicroCreditsPerCent, req.AmountMicro, now.UnixNano(), now.Add(time.Hour).UnixNano())
		return err
	}); err != nil {
		return CreditGrantResult{}, err
	}
	result, err := t.s.settlePaymentFacts(ctx, capability.PaymentEvent{EventID: "fixture-event:" + req.Reference, CheckoutID: checkout, PaymentID: req.Reference, Currency: "usd", AmountMinor: req.AmountMicro / MicroCreditsPerCent, Mode: capability.PaymentTest, Kind: capability.PaymentCheckoutPaid})
	if err != nil {
		return CreditGrantResult{}, err
	}
	if result.Team != t.team {
		return CreditGrantResult{}, ErrCreditGrantConflict
	}
	var grant CreditGrant
	err = t.s.inTx(ctx, func(tx *storeTx) error {
		var found bool
		var err error
		grant, found, err = paidGrantForPaymentTx(ctx, tx, req.Reference)
		if err == nil && !found {
			return ErrUnknownPayment
		}
		return err
	})
	return CreditGrantResult{Grant: grant, Created: !result.Replayed}, err
}

// TestOnlySeedTriggerWithRun seeds the reporting run before its owner projection.
func (t *Tenant) TestOnlySeedTriggerWithRun(ctx context.Context, v Trigger, r Run) error {
	if err := t.TestOnlySeedRun(ctx, r); err != nil {
		return err
	}
	return t.TestOnlySeedTrigger(ctx, v)
}

func (s *Store) defaultTenant() *Tenant { return &Tenant{s: s, team: DefaultTeam} }

type creatingPrincipalKey struct{}

// FixtureWithCreatingPrincipal associates reporting fixtures with a principal.
func FixtureWithCreatingPrincipal(ctx context.Context, principal string) context.Context {
	return context.WithValue(ctx, creatingPrincipalKey{}, principal)
}

func creatingPrincipal(ctx context.Context) string {
	principal, _ := ctx.Value(creatingPrincipalKey{}).(string)
	return principal
}

func nullableTimeNS(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixNano()
}
