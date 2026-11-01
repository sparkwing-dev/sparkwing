package store

import (
	"fmt"
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
	if _, err = s.exec(ctx, `UPDATE nodes SET requested_class=?,granted_class=?,fresh_node_wait=1,start_latency_ns=20000000000,execution_started_at=?,ready_at=?`, w.Class, w.Class, now, now); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err = s.queryRow(ctx, `SELECT COUNT(*) FROM nodes`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("fixture nodes: %d", count)
	}
	assertDesired(4)
	if _, err = s.exec(ctx, `UPDATE nodes SET start_latency_ns=1000000000`); err != nil {
		t.Fatal(err)
	}
	assertDesired(1)
	if _, err := s.exec(ctx, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,requested_class,start_latency_ns,execution_started_at) VALUES ('a','run','median-peer','pending',?,?,?,19000000000,?)`, []byte(`{}`), []byte(`{}`), w.Class, now); err != nil {
		t.Fatal(err)
	}
	assertDesired(1)
	if _, err := s.exec(ctx, `UPDATE nodes SET start_latency_ns=21000000000 WHERE node_id='median-peer'`); err != nil {
		t.Fatal(err)
	}
	assertDesired(4)
	if _, err := s.exec(ctx, `DELETE FROM nodes WHERE node_id='median-peer'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err = s.exec(ctx, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,requested_class,granted_class,fresh_node_wait,ready_at) VALUES ('a','run',?,'pending',?,?,?,'',1,?)`, fmt.Sprint("cold-", i), []byte(`{}`), []byte(`{}`), w.Class, now); err != nil {
			t.Fatal(err)
		}
	}
	assertDesired(4)
	if _, err = s.exec(ctx, `UPDATE nodes SET granted_class='sparkwing-4cpu' WHERE node_id='job'`); err != nil {
		t.Fatal(err)
	}
	metrics, e := op.WarmCapacity(ctx)
	if e != nil {
		t.Fatal(e)
	}
	c := metrics.Classes[0]
	if c.ColdStarts24h != 7 || c.ColdStarts7d != 7 || c.Upsizes24h != 1 || c.Upsizes7d != 1 || c.Start1h.P50Seconds == nil || *c.Start1h.P50Seconds != 1 || c.Start24h.P95Seconds == nil || *c.Start24h.P95Seconds != 1 {
		t.Fatalf("metrics: %+v", c)
	}
	if metrics.Classes[1].ColdStarts24h != 0 || metrics.Classes[1].Upsizes24h != 0 {
		t.Fatalf("upsizing changed requested-class metrics: %+v", metrics.Classes[1])
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
	if _, err = s.exec(ctx, `UPDATE nodes SET ready_at=?,execution_started_at=?`, now-int64(2*time.Hour), now-int64(2*time.Hour)); err != nil {
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
