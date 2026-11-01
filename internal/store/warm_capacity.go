package store

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

// WarmCapacitySettings shares the operator policy across transports and persistence.
type WarmCapacitySettings = settings.WarmCapacitySettings

// WarmStartPercentiles preserves the absence of start observations.
type WarmStartPercentiles struct {
	P50Seconds *float64 `json:"p50_seconds"`
	P95Seconds *float64 `json:"p95_seconds"`
}

// WarmCapacityClass reports one policy and its observed demand.
type WarmCapacityClass struct {
	WarmCapacitySettings
	Warm          int64                `json:"warm"`
	Desired       int64                `json:"desired"`
	Start1h       WarmStartPercentiles `json:"start_1h"`
	Start24h      WarmStartPercentiles `json:"start_24h"`
	ColdStarts24h int64                `json:"cold_starts_24h"`
	ColdStarts7d  int64                `json:"cold_starts_7d"`
	Upsizes24h    int64                `json:"upsizes_24h"`
	Upsizes7d     int64                `json:"upsizes_7d"`
}

// WarmCapacity reports policies and deployment activity.
type WarmCapacity struct {
	Classes        []WarmCapacityClass `json:"classes"`
	NewSignups24h  int64               `json:"new_signups_24h"`
	NewSignups7d   int64               `json:"new_signups_7d"`
	ActiveTeams24h int64               `json:"active_teams_24h"`
	ActiveTeams7d  int64               `json:"active_teams_7d"`
}

// WriteWarmCapacity replaces a policy under current operator authority.
func (o *Operator) WriteWarmCapacity(ctx context.Context, a capability.Authorized, w WarmCapacitySettings) error {
	if err := w.Validate(); err != nil {
		return errors.Join(ErrInvalidInput, err)
	}
	return operatorSettingsWrite(ctx, o, a, capability.OperatorLimits, "WriteWarmCapacity", func(tx *storeTx, _ capability.Principal) error {
		_, err := tx.execContext(ctx, `UPDATE warm_pool_settings SET mode=?,manual_count=?,auto_min=?,auto_max=?,start_time_target_seconds=? WHERE class=?`, w.Mode, w.ManualCount, w.AutoMin, w.AutoMax, w.StartTimeTargetSeconds, w.Class)
		return err
	})
}

// RecordWarmPoolObservations accepts ready, never-bound node counts from a current launcher.
func (o *Operator) RecordWarmPoolObservations(ctx context.Context, authority capability.AuthorityRef, warm map[string]int64) error {
	return o.s.inTx(ctx, func(tx *storeTx) error {
		chain, err := authorityChainTx(ctx, tx, authority, true)
		if err != nil {
			return err
		}
		claimant := chain[0].principal.Capability
		if authority.Kind != capability.AuthorityCapability || claimant.Audience != capability.Controller || !capability.CloudLauncher(claimant) {
			return bindingRefusal()
		}
		for class, count := range warm {
			if count < 0 {
				return ErrInvalidInput
			}
			if _, err = tx.execContext(ctx, `INSERT INTO warm_pool_observations(class,warm,observed_at) VALUES (?,?,?) ON CONFLICT(class) DO UPDATE SET warm=excluded.warm,observed_at=excluded.observed_at`, class, count, tx.now(ctx).UnixNano()); err != nil {
				return err
			}
		}
		_, err = authorityChainTx(ctx, tx, authority, false)
		return err
	})
}

func warmPercentiles(values []float64) WarmStartPercentiles {
	if len(values) == 0 {
		return WarmStartPercentiles{}
	}
	sort.Float64s(values)
	percentile := func(p float64) *float64 {
		i := p * float64(len(values)-1)
		lo := int(i)
		hi := int(math.Ceil(i))
		v := values[lo] + (values[hi]-values[lo])*(i-float64(lo))
		return &v
	}
	return WarmStartPercentiles{P50Seconds: percentile(.5), P95Seconds: percentile(.95)}
}

// WarmCapacity reads warm policies, start distributions and activity windows.
func (o *Operator) WarmCapacity(ctx context.Context) (WarmCapacity, error) {
	out := WarmCapacity{Classes: []WarmCapacityClass{}}
	rows, err := o.s.query(ctx, `SELECT s.class,s.mode,s.manual_count,s.auto_min,s.auto_max,s.start_time_target_seconds,v.warm,v.desired_replicas FROM warm_pool_settings s JOIN warm_pool_desired v ON v.hardware_class=s.class ORDER BY LENGTH(s.class),s.class`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var c WarmCapacityClass
		if err = rows.Scan(&c.Class, &c.Mode, &c.ManualCount, &c.AutoMin, &c.AutoMax, &c.StartTimeTargetSeconds, &c.Warm, &c.Desired); err != nil {
			break
		}
		out.Classes = append(out.Classes, c)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return out, err
	}
	now := o.s.now()
	day := now.Add(-24 * time.Hour).UnixNano()
	week := now.Add(-7 * 24 * time.Hour).UnixNano()
	hour := now.Add(-time.Hour).UnixNano()
	for i := range out.Classes {
		c := &out.Classes[i]
		var h, d []float64
		r, e := o.s.query(ctx, `SELECT execution_started_at,start_latency_ns FROM nodes WHERE requested_class=? AND execution_started_at>=? AND execution_started_at<=? AND start_latency_ns IS NOT NULL`, c.Class, day, now.UnixNano())
		if e != nil {
			return out, e
		}
		for r.Next() {
			var at, ns int64
			if e = r.Scan(&at, &ns); e != nil {
				break
			}
			d = append(d, float64(ns)/1e9)
			if at >= hour {
				h = append(h, float64(ns)/1e9)
			}
		}
		e = errors.Join(e, r.Err(), r.Close())
		if e != nil {
			return out, e
		}
		c.Start1h = warmPercentiles(h)
		c.Start24h = warmPercentiles(d)
		e = o.s.queryRow(ctx, `SELECT COALESCE(SUM(CASE WHEN fresh_node_wait=1 AND COALESCE(execution_started_at,ready_at)>=? THEN 1 ELSE 0 END),0), COALESCE(SUM(fresh_node_wait),0),COALESCE(SUM(CASE WHEN granted_class<>'' AND granted_class<>requested_class AND execution_started_at>=? THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN granted_class<>'' AND granted_class<>requested_class THEN 1 ELSE 0 END),0) FROM nodes WHERE requested_class=? AND COALESCE(execution_started_at,ready_at)>=? AND COALESCE(execution_started_at,ready_at)<=?`, day, day, c.Class, week, now.UnixNano()).Scan(&c.ColdStarts24h, &c.ColdStarts7d, &c.Upsizes24h, &c.Upsizes7d)
		if e != nil {
			return out, e
		}
	}
	err = o.s.queryRow(ctx, `SELECT COALESCE(SUM(CASE WHEN created_at>=? THEN 1 ELSE 0 END),0),COUNT(*) FROM accounts WHERE created_at>=? AND created_at<=?`, day, week, now.UnixNano()).Scan(&out.NewSignups24h, &out.NewSignups7d)
	if err != nil {
		return out, err
	}
	err = o.s.queryRow(ctx, `SELECT COUNT(DISTINCT CASE WHEN created_at>=? THEN team END),COUNT(DISTINCT team) FROM runs WHERE created_at>=? AND created_at<=?`, day, week, now.UnixNano()).Scan(&out.ActiveTeams24h, &out.ActiveTeams7d)
	return out, err
}
