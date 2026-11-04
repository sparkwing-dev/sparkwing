package store

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestWarmCapacityModesAndDemand(t *testing.T) { WarmCapacityModesAndDemand(t, authorityStore(t)) }

func WarmCapacityModesAndDemand(t *testing.T, s *Store) {
	rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1) ON CONFLICT(name) DO NOTHING`)
	ctx := t.Context()
	op := s.AsOperator()
	root := authorityRoot("warm-ops", "")
	root.Subject.Kind = capability.SubjectService
	root.Grants = []capability.Grant{capability.OperatorLimits}
	authorityPersist(t, s, root)
	a := capability.Authorized{Principal: capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}, Capability: root}}
	w := WarmCapacitySettings{Class: "sparkwing-2cpu", Mode: "off", StartTimeTargetSeconds: 10}
	got, err := op.WarmCapacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Classes) != 6 {
		t.Fatalf("classes: %+v", got)
	}
	for _, c := range got.Classes {
		if c.Mode != "off" || c.Desired != 0 {
			t.Fatalf("default: %+v", c)
		}
	}
	w.Mode = "manual"
	w.ManualCount = 3
	if err = op.WriteWarmCapacity(ctx, a, w); err != nil {
		t.Fatal(err)
	}
	assertDesired := func(want int64) {
		t.Helper()
		var got int64
		if e := s.queryRow(ctx, `SELECT desired_replicas FROM warm_pool_desired WHERE hardware_class=?`, w.Class).Scan(&got); e != nil {
			t.Fatal(e)
		}
		if got != want {
			t.Fatalf("desired %d, want %d", got, want)
		}
	}
	assertDesired(3)
	w.Mode = "auto"
	w.AutoMin = 1
	w.AutoMax = 5
	if err = op.WriteWarmCapacity(ctx, a, w); err != nil {
		t.Fatal(err)
	}
	assertDesired(1)
	if _, err = s.exec(ctx, `INSERT INTO warm_pool_observations(class,warm,observed_at) VALUES (?,3,?)`, w.Class, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days) VALUES ('a','run',1,'ci','running',?,'sealed',1,'gh:1','host','execute','account',1,'r30',30)`, []any{[]byte(`{"version":1,"request":{"git":{"repository":{"key":"gh:1"}}},"provenance":{"trust":"asserted","pipeline":"ci","writer":"account/fixture"}}`)}},
		{`INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run','job','pending',?,?)`, []any{[]byte(`{}`), []byte(`{}`)}},
	} {
		if _, err := s.exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	now := s.now().UnixNano()
	insertCapacity := func(id, kind string, started bool, latency int64, waited bool) {
		t.Helper()
		unit := kind
		if kind == "node" {
			unit = "job"
		}
		if _, err := s.exec(ctx, `INSERT INTO billing_awards(team,reservation_id,capability_prefix,run_id,node_id,generation,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at) VALUES ('a',?,?,'run',?,1,'a',1,'cloud','at_start',?,?,?)`, id, id, id, kind, unit, now); err != nil {
			t.Fatal(err)
		}
		var at, ns any
		if started {
			at, ns = now, latency
		}
		if _, err := s.exec(ctx, `INSERT INTO cloud_job_capacity(team,reservation_id,requested_class,granted_class,waited_for_fresh_node,claimed_at,started_at,claim_to_start_ns) VALUES ('a',?,?,?, ?,?,?,?)`, id, w.Class, w.Class, boolInt(waited), now-latency, at, ns); err != nil {
			t.Fatal(err)
		}
	}
	insertCapacity("job", "node", true, 20000000000, true)
	assertDesired(1)
	insertCapacity("live", "node", false, 0, true)
	assertDesired(4)
	if _, err = s.exec(ctx, `UPDATE cloud_job_capacity SET claim_to_start_ns=1000000000 WHERE reservation_id='job'`); err != nil {
		t.Fatal(err)
	}
	assertDesired(1)
	insertCapacity("median-peer", "node", true, 19000000000, false)
	assertDesired(1)
	if _, err := s.exec(ctx, `UPDATE cloud_job_capacity SET claim_to_start_ns=21000000000 WHERE reservation_id='median-peer'`); err != nil {
		t.Fatal(err)
	}
	assertDesired(4)
	if _, err := s.exec(ctx, `DELETE FROM cloud_job_capacity WHERE reservation_id='median-peer'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		insertCapacity(fmt.Sprint("cold-", i), "node", false, 0, true)
	}
	assertDesired(4)
	insertCapacity("preparation", "plan", true, 900000000000, true)
	assertDesired(4)
	if _, err = s.exec(ctx, `UPDATE cloud_job_capacity SET granted_class='sparkwing-4cpu' WHERE reservation_id='job'`); err != nil {
		t.Fatal(err)
	}
	metrics, e := op.WarmCapacity(ctx)
	if e != nil {
		t.Fatal(e)
	}
	c := metrics.Classes[0]
	if c.ColdStarts24h != 8 || c.ColdStarts7d != 8 || c.Upsizes24h != 1 || c.Upsizes7d != 1 || c.Start1h.P50Seconds == nil || *c.Start1h.P50Seconds != 1 || c.Start24h.P95Seconds == nil || *c.Start24h.P95Seconds != 1 {
		t.Fatalf("metrics: %+v", c)
	}
	if metrics.Classes[1].ColdStarts24h != 0 || metrics.Classes[1].Upsizes24h != 0 {
		t.Fatalf("upsizing changed requested-class metrics: %+v", metrics.Classes[1])
	}
	insertCapacity("pending-upsize", "node", false, 0, false)
	if _, err := s.exec(ctx, `UPDATE cloud_job_capacity SET granted_class='sparkwing-4cpu' WHERE reservation_id='pending-upsize'`); err != nil {
		t.Fatal(err)
	}
	metrics, err = op.WarmCapacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c := metrics.Classes[0]; c.Upsizes24h != 1 || c.Upsizes7d != 1 {
		t.Fatalf("unstarted grant counted as an upsize: %+v", c)
	}
	if _, err := s.exec(ctx, `UPDATE warm_pool_observations SET observed_at=?`, now-int64(121*time.Second)); err != nil {
		t.Fatal(err)
	}
	stale, err := op.WarmCapacity(ctx)
	if err != nil || stale.Classes[0].Warm != 0 {
		t.Fatalf("stale sample=%+v err=%v", stale, err)
	}
	w.AutoMax = 2
	if err = op.WriteWarmCapacity(ctx, a, w); err != nil {
		t.Fatal(err)
	}
	assertDesired(2)
	if _, err = s.exec(ctx, `UPDATE cloud_job_capacity SET claimed_at=?,started_at=CASE WHEN started_at IS NULL THEN NULL ELSE CAST(? AS BIGINT) END`, now-int64(2*time.Hour), now-int64(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	assertDesired(1)
	w.Mode = "off"
	if err = op.WriteWarmCapacity(ctx, a, w); err != nil {
		t.Fatal(err)
	}
	assertDesired(0)
	if _, err := s.exec(ctx, `UPDATE capabilities SET revoked_at=? WHERE prefix=?`, s.now().UnixNano(), root.Prefix); err != nil {
		t.Fatal(err)
	}
	if err = op.WriteWarmCapacity(ctx, a, w); err == nil {
		t.Fatal("revoked grant accepted")
	}
}

func TestWarmCapacityAutoReturnsToMinimumWithNobodyWaiting(t *testing.T) {
	WarmCapacityAutoReturnsToMinimumWithNobodyWaiting(t, authorityStore(t))
}

// WarmCapacityAutoReturnsToMinimumWithNobodyWaiting feeds each desired count back as the next observed warm
// count, as KEDA and Karpenter do every polling cycle, so a trigger fed by its own output shows up as a ratchet.
func WarmCapacityAutoReturnsToMinimumWithNobodyWaiting(t *testing.T, s *Store) {
	ctx := t.Context()
	const class = "sparkwing-4cpu"
	rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1) ON CONFLICT(name) DO NOTHING`)
	if _, err := s.exec(ctx, `UPDATE warm_pool_settings SET mode='auto',auto_min=1,auto_max=5,start_time_target_seconds=10 WHERE class=?`, class); err != nil {
		t.Fatal(err)
	}
	now := s.now().UnixNano()
	insert := func(id string, started bool, latency time.Duration, waited bool, ago time.Duration) {
		t.Helper()
		if _, err := s.exec(ctx, `INSERT INTO billing_awards(team,reservation_id,capability_prefix,run_id,node_id,generation,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at) VALUES ('a',?,?,'run',?,1,'a',1,'cloud','at_start','node','job',?)`, id, id, id, now); err != nil {
			t.Fatal(err)
		}
		at := now - int64(ago)
		var startedAt, ns any
		if started {
			startedAt, ns = at, int64(latency)
		}
		if _, err := s.exec(ctx, `INSERT INTO cloud_job_capacity(team,reservation_id,requested_class,granted_class,waited_for_fresh_node,claimed_at,started_at,claim_to_start_ns) VALUES ('a',?,?,?,?,?,?,?)`, id, class, class, boolInt(waited), at-int64(latency), startedAt, ns); err != nil {
			t.Fatal(err)
		}
	}
	cycles := func() []int64 {
		t.Helper()
		var warm int64
		var seen []int64
		for range 7 {
			if _, err := s.exec(ctx, `INSERT INTO warm_pool_observations(class,warm,observed_at) VALUES (?,?,?) ON CONFLICT(class) DO UPDATE SET warm=excluded.warm, observed_at=excluded.observed_at`, class, warm, s.now().UnixNano()); err != nil {
				t.Fatal(err)
			}
			if err := s.queryRow(ctx, `SELECT desired_replicas FROM warm_pool_desired WHERE hardware_class=?`, class).Scan(&warm); err != nil {
				t.Fatal(err)
			}
			seen = append(seen, warm)
		}
		return seen
	}
	steady := func(why string) {
		t.Helper()
		if got := cycles(); slices.Max(got) != 1 {
			t.Fatalf("%s: desired over repeated observations = %v, want the minimum 1 throughout", why, got)
		}
	}
	insert("slow", true, 20*time.Second, false, 40*time.Minute)
	steady("a slow start 40 minutes ago with nobody waiting")
	insert("cold-started", true, 40*time.Second, true, 5*time.Minute)
	steady("a cold wait that already started")
	insert("cold-waiting", false, 0, true, time.Minute)
	if got := cycles(); got[len(got)-1] != 5 || !slices.IsSorted(got) {
		t.Fatalf("desired while a job waits for a fresh node = %v, want growth to the maximum 5", got)
	}
	if _, err := s.exec(ctx, `UPDATE billing_awards SET settled_at=? WHERE reservation_id='cold-waiting'`, now); err != nil {
		t.Fatal(err)
	}
	steady("the waiting job's award settled before it started")
}

func TestWarmCapacityPolicyValidation(t *testing.T) {
	for _, w := range []WarmCapacitySettings{
		{Class: "sparkwing-2cpu", Mode: "manual", ManualCount: 2147483648, StartTimeTargetSeconds: 10},
		{Class: "sparkwing-2cpu", Mode: "auto", AutoMax: 2147483648, StartTimeTargetSeconds: 10},
	} {
		if w.Validate() == nil {
			t.Fatalf("accepted unsupported replica count %+v", w)
		}
	}
	for _, w := range []WarmCapacitySettings{{Class: "bad", Mode: "off", StartTimeTargetSeconds: 10}, {Class: "sparkwing-2cpu", Mode: "bad", StartTimeTargetSeconds: 10}, {Class: "sparkwing-2cpu", Mode: "auto", AutoMin: 2, AutoMax: 1, StartTimeTargetSeconds: 10}} {
		if w.Validate() == nil {
			t.Fatalf("accepted %+v", w)
		}
	}
}

func TestWarmPoolObservationAuthority(t *testing.T) {
	s := authorityStore(t)
	ctx := t.Context()
	root := authorityRoot("warm-launcher", "")
	root.Subject.Kind = capability.SubjectService
	root.Grants = []capability.Grant{capability.ClaimsLaunch}
	authorityPersist(t, s, root)
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
	if err := s.AsOperator().RecordWarmPoolObservations(ctx, ref, map[string]int64{"sparkwing-2cpu": 2}); err != nil {
		t.Fatal(err)
	}
	var warm int64
	if err := s.queryRow(ctx, `SELECT warm FROM warm_pool_desired WHERE hardware_class='sparkwing-2cpu'`).Scan(&warm); err != nil || warm != 2 {
		t.Fatalf("warm %d: %v", warm, err)
	}
	if _, err := s.exec(ctx, `UPDATE capabilities SET revoked_at=? WHERE prefix=?`, s.now().UnixNano(), root.Prefix); err != nil {
		t.Fatal(err)
	}
	if err := s.AsOperator().RecordWarmPoolObservations(ctx, ref, map[string]int64{"sparkwing-2cpu": 3}); err == nil {
		t.Fatal("revoked launcher accepted")
	}
}
