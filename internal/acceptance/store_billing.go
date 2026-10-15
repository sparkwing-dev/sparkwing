package acceptance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// StoreBillingBoundaryScenarios names store proofs without claiming native or provider execution.
func StoreBillingBoundaryScenarios() []string {
	return []string{"billing-ledger-survives-retention"}
}

// CheckStoreBillingBoundary checks committed financial rows across leased run retention and reopening.
func CheckStoreBillingBoundary(ctx context.Context, root, id string) (err error) {
	if id != "billing-ledger-survives-retention" {
		return fmt.Errorf("no store billing boundary proof for %q", id)
	}
	path := filepath.Join(root, "store-billing.db")
	s, err := store.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	db := s.DB()
	if err := seedStoreBillingBoundary(ctx, db); err != nil {
		return err
	}
	rate, free := int64(store.StorageBytesPerGB), int64(3)
	if _, err := s.SetCreditSettings(ctx, store.CreditSettingsUpdate{StorageRateMicroPerGBDay: &rate, StorageFreeAllowanceBytes: &free}); err != nil {
		return err
	}
	operator := s.AsOperator()
	lease, err := operator.AcquireSingletonLease(ctx, "maintenance", "billing-boundary", time.Hour)
	if err != nil {
		return err
	}
	first, err := operator.ChargeRetainedStorage(ctx, lease)
	if err != nil {
		return err
	}
	if len(first.Charges) != 0 {
		return fmt.Errorf("initial storage sample billed historical bytes: %+v", first)
	}
	if _, err := db.ExecContext(ctx, "UPDATE team_storage SET storage_charged_through=?", time.Now().Add(-24*time.Hour).UnixNano()); err != nil {
		return err
	}
	billed, err := operator.ChargeRetainedStorage(ctx, lease)
	if err != nil {
		return err
	}
	if len(billed.Charges) != 1 {
		return fmt.Errorf("storage charges=%d, want one", len(billed.Charges))
	}
	charge := billed.Charges[0]
	if charge.Team != store.DefaultTeam || charge.StorageBytes != store.StorageBytesPerGB || charge.RunID != "" || charge.Seconds < store.StorageChargeDaySeconds || charge.AmountMicro != rate*charge.Seconds/store.StorageChargeDaySeconds {
		return fmt.Errorf("storage charge disagrees with byte/second oracle: %+v", charge)
	}
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	before, err := billingFinancialSnapshot(ctx, db)
	if err != nil {
		return err
	}
	balance, err := tenant.CreditBalanceMicro(ctx)
	if err != nil {
		return err
	}
	if want := int64(10_000_000_000-30) - billed.ChargedMicro; balance != want {
		return fmt.Errorf("fixture balance=%d, want %d", balance, want)
	}
	result, err := operator.MaintainRetention(ctx, store.RetentionRequest{Lease: lease, Limit: 100})
	if err != nil {
		return err
	}
	if result.Runs != 1 {
		return fmt.Errorf("retention removed %d runs, want one", result.Runs)
	}
	for _, table := range []string{"runs", "log_streams", "log_segments"} {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("operational %s survived retention", table)
		}
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, err := store.Open(path)
	if err != nil {
		return err
	}
	s = reopened
	after, err := billingFinancialSnapshot(ctx, s.DB())
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before, after) {
		return errors.New("financial evidence changed during run retention or restart")
	}
	tenant, err = s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	got, err := tenant.CreditBalanceMicro(ctx)
	if err != nil {
		return err
	}
	if got != balance {
		return fmt.Errorf("retention balance=%d, want %d", got, balance)
	}
	return nil
}

func seedStoreBillingBoundary(ctx context.Context, db *sql.DB) error {
	team := string(store.DefaultTeam)
	old := time.Now().Add(-120 * 24 * time.Hour).UnixNano()
	fixtures := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,finished_at,log_retention_class,log_retention_days) VALUES (?,'old',1,'fixture','failed',?,'fixture',1,'gh:1','host','execute',?,'fixture',?,?,'r30',30)`, []any{team, []byte(`{}`), []byte(`[]`), old, old}},
		{`INSERT INTO log_streams(team,run_id,id,subject_kind,generation,unit_kind,prefix,retention_class,stored_bytes,created_at,updated_at) VALUES (?,'old','stream','owner_unit',1,'plan','fixture','r30',3,?,?)`, []any{team, old, old}},
		{`INSERT INTO log_segments(team,run_id,stream_id,generation,n,stored_bytes,object_key,sha256,start_offset,end_offset) VALUES (?,'old','stream',1,0,3,'fixture','fixture',0,3)`, []any{team}},
		{`INSERT INTO blobs(team,digest,size,created_at) VALUES (?,?,?,?)`, []any{team, strings.Repeat("a", 64), int64(store.StorageBytesPerGB), old}},
		{`INSERT INTO settings(key,value) VALUES ('operator.run_retention_days',?)`, []any{[]byte(`90`)}},
		{`INSERT INTO credit_grants(id,team,kind,amount_micro,reference,created_at) VALUES ('fund',?,'paid',10000000000,'payment',?)`, []any{team, old}},
		{`INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,kind,seconds,amount_micro,charged_at) VALUES ('compute',?,'old','job','award','usage',3,30,?)`, []any{team, old}},
		{`INSERT INTO credit_checkouts(id,team,session_id,payment_mode,payment_id,currency,amount_minor,amount_micro,opened_at,expires_at,paid_at,grant_id) VALUES ('checkout',?,'session','test','payment','usd',100000,10000000000,?,?,?,'fund')`, []any{team, old, old, old}},
		{`INSERT INTO payment_events(team,payment_mode,event_id,checkout_id,payment_id,event_kind,event_digest,grant_id,processed_at) VALUES (?,'test','event','checkout','payment','checkout_paid','fixture','fund',?)`, []any{team, old}},
		{`INSERT INTO credit_freezes(dispute_id,team,payment_id,created_at,released_at) VALUES ('freeze',?,'payment',?,?)`, []any{team, old, old}},
		{`INSERT INTO card_charges(id,team,amount_micro,cause,state,next_attempt_at,opened_at,closed_at) VALUES ('card',?,10,'settle','paid',?,?,?)`, []any{team, old, old, old}},
		{`INSERT INTO card_attempts(id,team,charge_id,kind,status,payment_intent,created_at,updated_at) VALUES ('attempt',?,'card','off_session','succeeded','payment',?,?)`, []any{team, old, old}},
		{`INSERT INTO card_refunds(team,payment_intent,reason,status,created_at) VALUES (?,'payment','fixture','succeeded',?)`, []any{team, old}},
		{`INSERT INTO payment_warnings(warning_id,payment_intent,created_at) VALUES ('warning','payment',?)`, []any{old}},
		{`INSERT INTO team_spend_days(team,day,amount_micro) VALUES (?,1,30)`, []any{team}},
		{`INSERT INTO card_spend_days(fingerprint,day,amount_micro) VALUES ('card',1,30)`, nil},
		{`INSERT INTO storage_run_usage(team,period,replica_id,run_id,bytes,updated_at) VALUES (?,'old','replica','old',3,?)`, []any{team, old}},
		{`INSERT INTO storage_month_usage(team,period,replica_id,bytes,updated_at) VALUES (?,'old','replica',3,?)`, []any{team, old}},
		{`INSERT INTO billing_awards(team,reservation_id,capability_prefix,run_id,node_id,generation,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at,reserved_amount_micro,settled_at) VALUES (?,'reservation','award','old','job',1,?,1,'cloud','at_start','node','job',?,30,?)`, []any{team, team, old, old}},
		{`INSERT INTO billable_intervals(team,id,run_id,node_id,attempt,generation,capability_prefix,reservation_id,payer_team,metered,executor_class,unit_kind,billing_unit,billing_start,started_at,finished_at,charged_through) VALUES (?,'interval','old','job',1,1,'award','reservation',?,1,'cloud','node','job','at_start',?,?,?)`, []any{team, team, old, old, old}},
	}
	for _, fixture := range fixtures {
		if _, err := db.ExecContext(ctx, fixture.query, fixture.args...); err != nil {
			return fmt.Errorf("billing fixture: %w", err)
		}
	}
	return nil
}

func billingFinancialSnapshot(ctx context.Context, db *sql.DB) (map[string][]string, error) {
	tables := append(store.FinancialTables(), "payment_events")
	snapshot := make(map[string][]string, len(tables))
	for _, table := range tables {
		rows, err := db.QueryContext(ctx, "SELECT * FROM "+table)
		if err != nil {
			return nil, err
		}
		columns, err := rows.Columns()
		if err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				return nil, errors.Join(err, rows.Close())
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				return nil, errors.Join(err, rows.Close())
			}
			snapshot[table] = append(snapshot[table], string(encoded))
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
		if len(snapshot[table]) == 0 {
			return nil, fmt.Errorf("missing financial fixture for %s", table)
		}
		slices.Sort(snapshot[table])
	}
	return snapshot, nil
}
