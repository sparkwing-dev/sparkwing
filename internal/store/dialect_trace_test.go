package store_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

type dialectCommand struct {
	Op        string `json:"op"`
	Reference string `json:"reference"`
	Amount    int64  `json:"amount"`
	Want      string `json:"want"`
	Balance   int64  `json:"balance"`
	Unchanged bool   `json:"unchanged"`
}

type dialectTraceState struct {
	Events, Audit, Awards, Leases, Grants, Charges, Runs, Nodes [][]string
	Balance                                                     int64
}

func TestStoreDialectRecordedTrace(t *testing.T) {
	data, err := os.ReadFile("testdata/dialect/lifecycle.json")
	if err != nil {
		t.Fatal(err)
	}
	var commands []dialectCommand
	if err := json.Unmarshal(data, &commands); err != nil {
		t.Fatal(err)
	}
	if len(commands) == 0 {
		t.Fatal("empty command trace")
	}
	var sqlite []dialectTraceState
	t.Run("sqlite", func(t *testing.T) { sqlite = replayDialectTrace(t, storetest.NewSQLite(t), commands) })
	for _, replay := range []struct {
		name   string
		target func(*testing.T) *storetest.Target
	}{{"sqlite_replay", storetest.NewSQLite}, {"postgres", storetest.NewPostgres}} {
		t.Run(replay.name, func(t *testing.T) {
			states := replayDialectTrace(t, replay.target(t), commands)
			if len(sqlite) != len(states) {
				t.Fatal("store did not execute every recorded command")
			}
			for i := range sqlite {
				if !reflect.DeepEqual(sqlite[i], states[i]) {
					t.Errorf("command %d (%s) replay mismatch:\nsqlite: %+v\n%s: %+v", i, commands[i].Op, sqlite[i], replay.name, states[i])
				}
			}
		})
	}
}

func replayDialectTrace(t *testing.T, target *storetest.Target, commands []dialectCommand) []dialectTraceState {
	t.Helper()
	s := target.Open(t, store.WithPolicies(sealStorePolicies))
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.SetTestClock(t, func() time.Time { return now })
	// A named owner award seeds the lifecycle; queue selection has its own reference model.
	tenant, plan, err := storePlanSealRequest(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	normalize := strings.NewReplacer(plan.RunID, "run", string(tenant.Team()), "team", plan.Actor.CapabilityPrefix, "owner", plan.Seal.Request.AuthorityRef.ID, "requester")
	before := readDialectTraceState(t, s, tenant, normalize)
	if len(before.Awards) != 1 {
		t.Fatalf("fixture awards = %v, want one", before.Awards)
	}
	states := make([]dialectTraceState, 0, len(commands))
	for i, command := range commands {
		now = now.Add(time.Second)
		outcome := "ok"
		var commandErr error
		switch command.Op {
		case "accept":
			_, commandErr = tenant.AcceptPlan(t.Context(), plan)
		case "invalid_plan":
			invalid := plan
			invalid.Seal.Plan = json.RawMessage(`{"jobs":[{"id":"broken","needs":["missing"]}]}`)
			_, commandErr = tenant.AcceptPlan(t.Context(), invalid)
		case "grant":
			var result store.CreditGrantResult
			result, commandErr = tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: "free", Reference: command.Reference, AmountMicro: command.Amount, CreatedBy: "operator"})
			if commandErr == nil {
				outcome = "replayed"
				if result.Created {
					outcome = "created"
				}
			}
		case "start":
			_, commandErr = tenant.TransitionRun(t.Context(), store.RunTransition{RunID: runspec.RunID(plan.RunID), Actor: plan.Actor, To: store.RunRunning, Cause: store.RunStart})
		case "renew":
			_, commandErr = s.AsOperator().RenewUnit(t.Context(), plan.Actor.CapabilityPrefix, false)
		default:
			t.Fatalf("unknown recorded command %q", command.Op)
		}
		if commandErr != nil {
			outcome = dialectTraceError(commandErr)
		}
		if outcome != command.Want {
			t.Fatalf("command %d (%s): outcome %q, want %q: %v", i, command.Op, outcome, command.Want, commandErr)
		}
		after := readDialectTraceState(t, s, tenant, normalize)
		if after.Balance != command.Balance {
			t.Fatalf("command %d: balance %d, want %d", i, after.Balance, command.Balance)
		}
		if command.Unchanged && !reflect.DeepEqual(before, after) {
			t.Fatalf("command %d (%s) changed state on replay/refusal:\nbefore: %+v\nafter: %+v", i, command.Op, before, after)
		}
		states = append(states, after)
		before = after
	}
	if len(before.Events) != 1 || before.Events[0][2] != "run_started" || len(before.Grants) != 2 || before.Runs[0][0] != "running" {
		t.Fatalf("trace did not exercise events and ledger: %+v", before)
	}
	return states
}

func dialectTraceError(err error) string {
	switch {
	case errors.Is(err, store.ErrCreditGrantConflict):
		return "grant_conflict"
	case errors.Is(err, store.ErrInvalidInput):
		return "invalid_input"
	}
	var api *apierr.Error
	if errors.As(err, &api) {
		return string(api.Code)
	}
	return "unexpected: " + err.Error()
}

func readDialectTraceState(t *testing.T, s *store.Store, tenant *store.Tenant, normalize *strings.Replacer) dialectTraceState {
	t.Helper()
	query := func(sql string) [][]string {
		t.Helper()
		rows, err := s.DB().QueryContext(t.Context(), sql)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		result := make([][]string, 0)
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			row := make([]string, len(columns))
			for i, value := range values {
				if bytes, ok := value.([]byte); ok {
					value = string(bytes)
				}
				row[i] = normalize.Replace(fmt.Sprint(value))
			}
			result = append(result, row)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	balance, err := tenant.CreditBalanceMicro(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return dialectTraceState{
		Events:  query(`SELECT seq,node_id,kind,state_seq,ts,payload FROM events ORDER BY seq`),
		Audit:   query(`SELECT kind,ts,actor_is_account,subject_is_account FROM audit_events ORDER BY ts,kind`),
		Leases:  query(`SELECT unit_kind,unit_id,award_purpose,delivery_purpose,bind_kind,bind_run,bind_node,bind_generation,bind_holder,created_at,expires_at,revoked_at,last_used_at FROM capabilities WHERE issuance='award' ORDER BY unit_kind,unit_id,bind_generation`),
		Awards:  query(`SELECT node_id,generation,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at,settled_at,stop_reason,drain_deadline_at FROM billing_awards ORDER BY unit_kind,node_id,generation`),
		Grants:  query(`SELECT kind,amount_micro,reference,created_by,created_at,reverses FROM credit_grants ORDER BY reference`),
		Charges: query(`SELECT node_id,kind,seconds,amount_micro,principal,storage_bytes,cpu_class,rate_micro_per_second,charged_at FROM credit_charges ORDER BY charged_at,kind,node_id`),
		Runs:    query(`SELECT status,cancel_reason FROM runs ORDER BY number`),
		Nodes:   query(`SELECT node_id,status,attempt,state_seq FROM nodes ORDER BY node_id`),
		Balance: balance,
	}
}
