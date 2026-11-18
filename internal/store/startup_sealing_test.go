package store_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func startupFixture(t *testing.T, count int) (*store.Store, *secrets.Cipher, *storetest.Target) {
	t.Helper()
	target := storetest.NewSQLite(t)
	if os.Getenv("SPARKWING_TEST_STORE") == "postgres" {
		target = storetest.NewPostgres(t)
	}
	st := target.Open(t)
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte{7}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := st.DB().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := range count {
		_, err = tx.ExecContext(t.Context(), storetest.Rebind(st, `INSERT INTO cron_schedules(team,id,repo_path,repo_key,pipeline,cron,tz,overlap,catch_up_ns,armed_at,updated_at,cursor_at,args) VALUES (?,?, ?,'git:example.com/project','nightly','0 * * * *','UTC','skip',0,1,1,1,?)`), store.DefaultTeam, fmt.Sprintf("schedule-%05d", i), fmt.Sprintf("/repo/%d", i), `{"token":"startup-canary-739"}`)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return st, cipher, target
}

func startupRaw(t *testing.T, st *store.Store) string {
	t.Helper()
	var raw string
	if err := st.DB().QueryRowContext(t.Context(), `SELECT args FROM cron_schedules ORDER BY id LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestStartupSealingPlaintextAndSecondPass(t *testing.T) {
	st, cipher, _ := startupFixture(t, 3)
	result, err := startupServePass(t, st, cipher)
	if err != nil || result.PlaintextRunArguments != 3 {
		t.Fatalf("first pass: %+v %v", result, err)
	}
	first := startupRaw(t, st)
	if bytes.Contains([]byte(first), []byte("startup-canary-739")) {
		t.Fatal("plaintext persisted")
	}
	result, err = startupServePass(t, st, cipher)
	if err != nil || result.PlaintextRunArguments != 0 || result.AlreadySealed != 3 || first != startupRaw(t, st) {
		t.Fatalf("second pass changed rows: %+v %v", result, err)
	}
	if err := st.AsOperator().RecordStartupSealing(t.Context(), result, "success"); err != nil {
		t.Fatal(err)
	}
	var attrs, actor string
	if err := st.DB().QueryRowContext(t.Context(), `SELECT attrs,actor FROM audit_events WHERE kind='secrets.startup_sealed'`).Scan(&attrs, &actor); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(attrs), []byte("startup-canary")) || actor != "controller" {
		t.Fatalf("audit: %s %s", actor, attrs)
	}
}

func TestStartupSealingConcurrentPasses(t *testing.T) {
	st, cipher, target := startupFixture(t, 201)
	second := target.Open(t)
	start := make(chan struct{})
	results := make(chan store.SealedRotation, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, instance := range []*store.Store{st, second} {
		wg.Go(func() {
			<-start
			result, err := instance.AsOperator().SealPlaintextValues(t.Context(), cipher)
			results <- result
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	sealed := 0
	for result := range results {
		sealed += result.PlaintextRunArguments
	}
	if sealed != 201 {
		t.Fatalf("sealed %d documents, want 201", sealed)
	}
}

func TestStartupSealingUnreadableAndPreviousKey(t *testing.T) {
	st, old, _ := startupFixture(t, 6)
	if _, err := st.AsOperator().SealPlaintextValues(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	current, err := secrets.NewCipherWithPrevious(bytes.Repeat([]byte{8}, secrets.KeySize), bytes.Repeat([]byte{7}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	before := startupRaw(t, st)
	if _, err := st.DB().ExecContext(t.Context(), `UPDATE cron_schedules SET args='{' WHERE id='schedule-00002'`); err != nil {
		t.Fatal(err)
	}

	if _, err := st.DB().ExecContext(t.Context(), `UPDATE cron_schedules SET args='{"argument_document_version":1,"body":"","sealed_arguments":"enc:v3:AAAA"}' WHERE id='schedule-00003'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(t.Context(), `UPDATE cron_schedules SET args='{"token":"startup-canary-739"}' WHERE id='schedule-00004'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(t.Context(), `UPDATE cron_schedules SET args='{"argument_document_version":1,"sealed_arguments":42}' WHERE id='schedule-00005'`); err != nil {
		t.Fatal(err)
	}
	result, err := st.AsOperator().SealPlaintextValues(t.Context(), current)
	if err != nil || result.PreviousKeyOnly != 2 || len(result.Unreadable) != 3 || result.PlaintextRunArguments != 1 || before != startupRaw(t, st) {
		t.Fatalf("result: %+v %v", result, err)
	}
	a := teststore.OperatorAuthorized(t, st, "")
	route, _ := capability.LookupRoute("POST", "/api/v1/operator/secrets/rotate")
	a.Access = route.Access[0]
	verified, err := st.AsOperator().RotateSealedValues(t.Context(), a, current, true)
	if err != nil || len(verified.Unreadable) != len(result.Unreadable) || verified.PreviousKeyOnly != 2 {
		t.Fatalf("verify disagrees: %+v %v", verified, err)
	}
}

func TestStartupSealingCancelledPass(t *testing.T) {
	st, cipher, _ := startupFixture(t, 201)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := st.AsOperator().SealPlaintextValues(ctx, cipher); err == nil {
		t.Fatal("cancelled scan succeeded")
	}
	result, err := st.AsOperator().SealPlaintextValues(t.Context(), cipher)
	if err != nil || result.PlaintextRunArguments != 201 {
		t.Fatalf("cancelled pass changed rows: %+v %v", result, err)
	}
}

func TestStartupSealingBootCost(t *testing.T) {
	st, cipher, _ := startupFixture(t, 3000)
	for _, phase := range []string{"plaintext", "sealed", "sealed-again"} {
		start := time.Now()
		result, err := st.AsOperator().SealPlaintextValues(t.Context(), cipher)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		if phase == "plaintext" && result.PlaintextRunArguments != 3000 || phase != "plaintext" && result.AlreadySealed != 3000 {
			t.Fatalf("incomplete boot scan: %+v", result)
		}
		t.Logf("boot_scan phase=%s rows=3000 elapsed=%s sealed=%d already_sealed=%d", phase, elapsed, result.PlaintextRunArguments, result.AlreadySealed)
	}
}

func TestStartupSealingRacesOperator(t *testing.T) {
	st, cipher, _ := startupFixture(t, 201)
	a := teststore.OperatorAuthorized(t, st, "")
	route, ok := capability.LookupRoute("POST", "/api/v1/operator/secrets/rotate")
	if !ok {
		t.Fatal("rotation route missing")
	}
	a.Access = route.Access[0]
	start := make(chan struct{})
	errs := make(chan error, 2)
	startupResult := make(chan store.SealedRotation, 1)
	go func() {
		<-start
		result, err := st.AsOperator().SealPlaintextValues(t.Context(), cipher)
		startupResult <- result
		errs <- err
	}()
	go func() {
		<-start
		_, err := st.AsOperator().RotateSealedValues(t.Context(), a, cipher, false)
		errs <- err
	}()
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	startup := <-startupResult
	if startup.PlaintextRunArguments+startup.AlreadySealed != 201 {
		t.Fatalf("startup did not scan every racing row: %+v", startup)
	}
	result, err := st.AsOperator().RotateSealedValues(t.Context(), a, cipher, true)
	if err != nil || result.PlaintextRunArguments != 0 || len(result.Unreadable) != 0 || result.RunArguments != 201 {
		t.Fatalf("racing rotation damaged rows: %+v %v", result, err)
	}
}

func TestStartupSealingCoversEveryArgumentColumn(t *testing.T) {
	st, cipher, _ := startupFixture(t, 1)
	ctx := t.Context()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st, query), args...); err != nil {
			t.Fatal(err)
		}
	}
	body := `{"token":"startup-canary-739"}`
	exec(`UPDATE cron_schedules SET override_args=?,override_base=?,consent_json=?`, body, body, body)
	exec(`INSERT INTO cron_fires(team,id,schedule_id,due_at,decided_at,outcome,args) VALUES (?,'fire','schedule-00000',1,1,'missed',?)`, store.DefaultTeam, body)
	cronRun(t, st, "startup-run")
	exec(`UPDATE runs SET sealed_at=1,spec_hash='spec',plan_hash='plan',request_json=?,spec_json=?,plan_json=? WHERE id='startup-run'`, []byte(`{"request":{"requested_args":{"token":"startup-canary-739"}}}`), []byte(`{"resolved":{"args":{"token":"startup-canary-739"}}}`), []byte(`{"jobs":[{"arguments":[{"default":"startup-canary-739"}]}]}`))
	exec(`INSERT INTO nodes(team,run_id,node_id,status,execution_json,spec_json,expansion_body,expansion_registration) VALUES (?,'startup-run','job','pending',?, ?,?,?)`, store.DefaultTeam, []byte(`{}`), []byte(`{"arguments":[{"default":"startup-canary-739"}]}`), []byte(`{"jobs":[{"arguments":[{"default":"startup-canary-739"}]}]}`), []byte(`{"defaults":[{"raw":"startup-canary-739"}]}`))
	result, err := st.AsOperator().SealPlaintextValues(ctx, cipher)
	if err != nil || result.PlaintextRunArguments != 11 {
		t.Fatalf("all columns: %+v %v", result, err)
	}
	for _, query := range []string{`SELECT args,override_args,override_base,consent_json FROM cron_schedules`, `SELECT args FROM cron_fires`, `SELECT request_json,spec_json,plan_json FROM runs`, `SELECT spec_json,expansion_body,expansion_registration FROM nodes`} {
		rows, err := st.DB().QueryContext(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([][]byte, len(columns))
			dest := make([]any, len(columns))
			for i := range dest {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			for _, value := range values {
				if bytes.Contains(value, []byte("startup-canary-739")) {
					t.Fatalf("plaintext remains: %s", query)
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	result, err = st.AsOperator().SealPlaintextValues(ctx, cipher)
	if err != nil || result.PlaintextRunArguments != 0 || result.AlreadySealed != 11 {
		t.Fatalf("all columns second pass: %+v %v", result, err)
	}
}

type failingStartupCipher struct {
	*secrets.Cipher
	calls int
}

func (c *failingStartupCipher) SealBound(p secrets.Purpose, team, name, scope string, shared, masked bool, plain string) (string, error) {
	c.calls++
	if c.calls == 201 {
		return "", errors.New("injected batch failure")
	}
	return c.Cipher.SealBound(p, team, name, scope, shared, masked, plain)
}

func TestStartupSealingFailurePreservesCommittedCounts(t *testing.T) {
	st, cipher, _ := startupFixture(t, 201)
	result, err := st.AsOperator().SealPlaintextValues(t.Context(), &failingStartupCipher{Cipher: cipher})
	if err == nil || result.PlaintextRunArguments != 200 {
		t.Fatalf("partial counts: %+v %v", result, err)
	}
	result, err = st.AsOperator().SealPlaintextValues(t.Context(), cipher)
	if err != nil || result.PlaintextRunArguments != 1 || result.AlreadySealed != 200 {
		t.Fatalf("retry: %+v %v", result, err)
	}
}

type startupPassLog struct {
	slog.Handler
	results chan startupPassResult
}

type startupPassResult struct {
	counts  store.SealedRotation
	outcome string
}

func (h startupPassLog) Handle(_ context.Context, record slog.Record) error {
	if record.Message != "startup plaintext sealing" {
		return nil
	}
	var result startupPassResult
	record.Attrs(func(attr slog.Attr) bool {
		switch attr.Key {
		case "sealed":
			result.counts.PlaintextRunArguments = int(attr.Value.Int64())
		case "already_sealed":
			result.counts.AlreadySealed = int(attr.Value.Int64())
		case "outcome":
			result.outcome = attr.Value.String()
		}
		return true
	})
	h.results <- result
	return nil
}

func startupServePass(t *testing.T, st *store.Store, cipher *secrets.Cipher) (store.SealedRotation, error) {
	t.Helper()
	results := make(chan startupPassResult, 1)
	logger := slog.New(startupPassLog{Handler: slog.NewTextHandler(io.Discard, nil), results: results})
	server := controller.New(st, logger).WithSecretsCipher(cipher).WithEngineController(controller.NewEngineController(st, controller.EngineController{RunnerPolicy: &controller.SignedRunnerAdmission{}}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return store.SealedRotation{}, err
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- controller.ServeWith(ctx, server, controller.Listeners{API: ln}) }()
	select {
	case result := <-results:
		cancel()
		if err := <-done; err != nil {
			return result.counts, err
		}
		if result.outcome != "success" {
			return result.counts, fmt.Errorf("startup outcome: %s", result.outcome)
		}
		return result.counts, nil
	case err := <-done:
		cancel()
		if err == nil {
			err = errors.New("controller exited before startup sealing")
		}
		return store.SealedRotation{}, err
	case <-t.Context().Done():
		cancel()
		<-done
		return store.SealedRotation{}, t.Context().Err()
	}
}

type cancellingStartupCipher struct {
	*secrets.Cipher
	cancel context.CancelFunc
}

func (c cancellingStartupCipher) SealBound(p secrets.Purpose, team, name, scope string, shared, masked bool, plain string) (string, error) {
	c.cancel()
	return c.Cipher.SealBound(p, team, name, scope, shared, masked, plain)
}

func TestStartupSealingCancellationDuringBatch(t *testing.T) {
	st, cipher, _ := startupFixture(t, 201)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result, err := st.AsOperator().SealPlaintextValues(ctx, cancellingStartupCipher{Cipher: cipher, cancel: cancel})
	if err == nil || result.PlaintextRunArguments != 0 {
		t.Fatalf("cancelled batch committed: %+v %v", result, err)
	}
	result, err = st.AsOperator().SealPlaintextValues(t.Context(), cipher)
	if err != nil || result.PlaintextRunArguments != 201 {
		t.Fatalf("cancelled batch lost plaintext: %+v %v", result, err)
	}
}
