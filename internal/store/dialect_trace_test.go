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
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
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
	Events, Audit, Awards, Leases, Grants, Charges, Runs, Nodes, Triggers, Capacity, Intervals [][]string
	Balance                                                                                    int64
}

type dialectTraceResult struct {
	Accepted  store.AcceptedPlan
	Grant     store.CreditGrantResult
	Applied   runstate.Applied
	Beat      runstate.LeaseBeat
	Admission runstate.AcquireSlotResponse
	Error     string
}

type dialectTraceStep struct {
	Result dialectTraceResult
	State  dialectTraceState
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
	var sqlite []dialectTraceStep
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

func replayDialectTrace(t *testing.T, target *storetest.Target, commands []dialectCommand) []dialectTraceStep {
	t.Helper()
	s := target.Open(t, store.WithPolicies(sealStorePolicies))
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.SetTestClock(t, func() time.Time { return now })
	// A named owner award seeds the lifecycle; queue selection has its own reference model.
	tenant, plan, err := storePlanSealRequestFor(t.Context(), s, string(acceptanceTeam), true, func(_ *store.IntakeRequest, doc *nodeproto.PlanDoc) {
		doc.Concurrency = []nodeproto.ConcurrencySpec{admissionGroup(1, 1)}
		tenant, err := s.ForTeam(t.Context(), acceptanceTeam)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: "free", Reference: "fixture", AmountMicro: 1000000, CreatedBy: "operator"}); err != nil {
			t.Fatal(err)
		}
		rates := store.CreditRateTable{{Cores: 2, MicroPerSecond: 10}, {Cores: 8, MicroPerSecond: 10}}
		if _, err := s.AsOperator().SetCreditSettings(t.Context(), store.CreditSettingsUpdate{RateTable: &rates}); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	identities := []string{plan.Seal.Request.Request.Actor.ID, "account", plan.RunID, "run", string(tenant.Team()), "team", plan.Actor.CapabilityPrefix, "owner", plan.Seal.Request.AuthorityRef.ID, "requester", string(plan.Seal.Request.Hash), "request-hash"}
	normalize := strings.NewReplacer(identities...)
	var specHash runspec.Hash
	grantIDs := map[string]string{}
	before := readDialectTraceState(t, s, tenant, normalize)
	if len(before.Awards) != 1 {
		t.Fatalf("fixture awards = %v, want one", before.Awards)
	}
	states := make([]dialectTraceStep, 0, len(commands))
	for i, command := range commands {
		now = now.Add(time.Second)
		outcome := "ok"
		var commandErr error
		var result dialectTraceResult
		switch command.Op {
		case "accept":
			result.Accepted, commandErr = tenant.AcceptPlan(t.Context(), plan)
			if commandErr == nil {
				if specHash == "" {
					input := plan.Seal
					input.Now = now
					expected, err := runspec.SealSpec(input)
					if err != nil {
						t.Fatal(err)
					}
					specHash = expected.Hash
					identities = append(identities, string(specHash), "spec-hash")
				}
				if result.Accepted.Spec.Hash != specHash {
					t.Fatal("acceptance returned a different spec digest")
				}
			}
		case "invalid_plan":
			invalid := plan
			invalid.Seal.Plan = json.RawMessage(`{"jobs":[{"id":"broken","needs":["missing"]}]}`)
			result.Accepted, commandErr = tenant.AcceptPlan(t.Context(), invalid)
		case "grant":
			result.Grant, commandErr = tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: "free", Reference: command.Reference, AmountMicro: command.Amount, CreatedBy: "operator"})
			if commandErr == nil {
				outcome = "replayed"
				if result.Grant.Created {
					grantIDs[command.Reference] = result.Grant.Grant.ID
					outcome = "created"
				}
			}
			if commandErr == nil {
				if result.Grant.Grant.ID != grantIDs[command.Reference] {
					t.Fatal("grant replay changed identity")
				}
				identities = append(identities, result.Grant.Grant.ID, "grant:"+command.Reference)
			}
		case "admit":
			result.Admission, commandErr = tenant.MayStart(t.Context(), runstate.CapacityBinding{RunID: plan.RunID, Generation: int64(plan.Actor.Owner.Generation)}, plan.Actor)
			if commandErr == nil && result.Admission.Kind != runstate.AcquireGranted {
				t.Fatalf("capacity admission: %+v", result.Admission)
			}
		case "start":
			result.Applied, commandErr = tenant.TransitionRun(t.Context(), runstate.RunTransition{RunID: runspec.RunID(plan.RunID), Actor: plan.Actor, To: runstate.RunRunning, Cause: runstate.RunStart})
		case "renew":
			result.Beat, commandErr = s.AsOperator().RenewUnit(t.Context(), plan.Actor.CapabilityPrefix, true)
			if commandErr == nil {
				// safety: the admitted slice covers the host minimum, so this renewal funds one slice past the compute it starts.
				want := now.Add(60 * time.Second)
				var deadline int64
				if err := s.DB().QueryRowContext(t.Context(), `SELECT lease_expires_at FROM triggers`).Scan(&deadline); err != nil {
					t.Fatal(err)
				}
				var previous int64
				if _, err := fmt.Sscan(before.Triggers[0][3], &previous); err != nil {
					t.Fatal(err)
				}
				if !result.Beat.ExpiresAt.Equal(want) || deadline != want.UnixNano() || deadline <= previous {
					t.Fatalf("renewal did not advance deadline: beat=%+v, persisted=%d, previous=%d, want=%s", result.Beat, deadline, previous, want)
				}
			}
		case "cancel":
			result.Applied, commandErr = tenant.TransitionRun(t.Context(), runstate.RunTransition{RunID: runspec.RunID(plan.RunID), Actor: runstate.Actor{Kind: runstate.ActorOperator, Authority: plan.Seal.Request.AuthorityRef}, To: runstate.RunCancelled, Cause: runstate.RunCancel, Reason: "trace"})
		case "settle":
			result.Applied, commandErr = tenant.FinishRunAndUnit(t.Context(), runstate.FinishRunRequest{Transition: runstate.RunCompletion{RunID: runspec.RunID(plan.RunID), Actor: plan.Actor, Cause: runstate.RunFinish}, Unit: plan.Actor.Owner})
		default:
			t.Fatalf("unknown recorded command %q", command.Op)
		}
		if commandErr != nil {
			outcome = dialectTraceError(commandErr)
			result.Error = outcome
		}
		if outcome != command.Want {
			t.Fatalf("command %d (%s): outcome %q, want %q: %v", i, command.Op, outcome, command.Want, commandErr)
		}
		normalize = strings.NewReplacer(identities...)
		after := readDialectTraceState(t, s, tenant, normalize)
		if command.Op == "renew" && (len(after.Capacity) != 1 || after.Capacity[0][4] != fmt.Sprint(result.Beat.ExpiresAt.UnixNano())) {
			t.Fatalf("renewal did not advance retained capacity: %+v", after.Capacity)
		}
		if after.Balance != command.Balance {
			t.Fatalf("command %d: balance %d, want %d", i, after.Balance, command.Balance)
		}
		if command.Unchanged && !reflect.DeepEqual(before, after) {
			t.Fatalf("command %d (%s) changed state on replay/refusal:\nbefore: %+v\nafter: %+v", i, command.Op, before, after)
		}
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(normalize.Replace(string(raw))), &result); err != nil {
			t.Fatal(err)
		}
		states = append(states, dialectTraceStep{Result: result, State: after})
		before = after
	}
	if len(before.Events) < 3 || len(before.Grants) != 3 || len(before.Charges) < 2 || before.Runs[0][0] != "cancelled" || before.Awards[0][8] == "<nil>" {
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
		Events:    query(`SELECT seq,node_id,kind,state_seq,ts,payload FROM events ORDER BY seq`),
		Audit:     query(`SELECT kind,ts,actor_is_account,subject_is_account FROM audit_events ORDER BY ts,kind`),
		Leases:    query(`SELECT unit_kind,unit_id,award_purpose,delivery_purpose,bind_kind,bind_run,bind_node,bind_generation,bind_holder,created_at,expires_at,revoked_at,last_used_at FROM capabilities WHERE issuance='award' ORDER BY unit_kind,unit_id,bind_generation`),
		Awards:    query(`SELECT node_id,generation,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at,settled_at,stop_reason,drain_deadline_at FROM billing_awards ORDER BY unit_kind,node_id,generation`),
		Grants:    query(`SELECT kind,amount_micro,reference,created_by,created_at,reverses FROM credit_grants ORDER BY reference`),
		Charges:   query(`SELECT node_id,kind,seconds,amount_micro,principal,storage_bytes,cpu_class,rate_micro_per_second,charged_at FROM credit_charges ORDER BY charged_at,kind,node_id`),
		Runs:      query(`SELECT status,cancel_reason,started_at,finished_at,cancel_requested_at,state_seq FROM runs ORDER BY number`),
		Nodes:     query(`SELECT node_id,status,attempt,state_seq,claimed_by,execution_started_at,lease_expires_at,last_heartbeat,generation,outcome,ready_at,started_at,finished_at,cancel_requested_at,cancel_reason FROM nodes ORDER BY node_id`),
		Triggers:  query(`SELECT unit_kind,status,claimed_at,lease_expires_at,started_at,finished_at,generation,claimed_by,cancel_requested_at,drain_deadline_at FROM triggers ORDER BY id`),
		Capacity:  query(`SELECT key,holder_id,node_id,claimed_at,lease_expires_at,generation,cost,declared_capacity,superseded FROM concurrency_holders ORDER BY key,holder_id`),
		Intervals: query(`SELECT unit_kind,started_at,finished_at,charged_through FROM billable_intervals ORDER BY started_at,id`),
		Balance:   balance,
	}
}
