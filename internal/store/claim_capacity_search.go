package store

import (
	"context"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type claimCapacityKey struct {
	team Team
	key  string
}

// safety: budget hints belong to one search snapshot; award and admission transactions recompute them.
type claimCapacitySearch struct {
	accounting map[claimCapacityKey]concurrencyAccounting
	blocked    []claimBlockedGroup
}

type claimBlockedGroup struct {
	team                   Team
	scope, identity, group string
	capacity, cost         int
	allDemands             bool
	holders                []ConcurrencyHolder
}

func (s *claimCapacitySearch) read(ctx context.Context, tx *storeTx, team Team, key string, now int64) (concurrencyAccounting, error) {
	if s == nil {
		return txConcurrencyAccounting(ctx, tx, team, key, now)
	}
	cacheKey := claimCapacityKey{team, key}
	if accounting, found := s.accounting[cacheKey]; found {
		return accounting, nil
	}
	accounting, err := txConcurrencyAccounting(ctx, tx, team, key, now)
	if err == nil {
		s.accounting[cacheKey] = accounting
	}
	return accounting, err
}

func (s *claimCapacitySearch) block(team Team, key string, capacity, cost int, acct concurrencyAccounting) {
	if s == nil || fitsBudget(acct.used, cost, acct.effectiveCapacity(capacity)) {
		return
	}
	_, scope, identity, group, ok := runspec.ParseConcurrencyKey(key)
	if !ok {
		return
	}
	allDemands := acct.floor > 0 && !fitsBudget(acct.used, 1, acct.floor)
	for _, previous := range s.blocked {
		if previous.team == team && previous.scope == scope && previous.identity == identity && previous.group == group && (previous.allDemands || previous.capacity == capacity && previous.cost == cost) {
			return
		}
	}
	// safety: every positive cost exceeds a saturated holder floor, whatever a sibling's declared capacity.
	s.blocked = append(s.blocked, claimBlockedGroup{team: team, scope: scope, identity: identity, group: group, capacity: capacity, cost: cost, allDemands: allDemands, holders: acct.holders})
}

func (q *claimSQL) capacityExclusionSQL(alias string) string {
	if len(q.capacity.blocked) == 0 {
		return "1=1"
	}
	groups := `COALESCE(` + q.at(q.blob(alias+`.execution_json`), "concurrency") + `,'[]')`
	// safety: unseen and expired timed jobs must reach the fenced clock writer; live clocks need no observation.
	ms := q.number(q.at("timed_group.value", "queue_timeout_ms"))
	timed := `EXISTS (SELECT 1 FROM ` + q.array(groups, "timed_group") + ` WHERE ` + ms + `>0 AND (` + alias + `.group_wait_started_at IS NULL OR ` + ms + `<=(clock.now-` + alias + `.group_wait_started_at)/1000000))`
	var blocked []string
	for _, group := range q.capacity.blocked {
		terms := []string{alias + `.team=` + q.param(string(group.team))}
		if group.scope == "run" {
			terms = append(terms, alias+`.run_id=`+q.param(group.identity))
		}
		for _, holder := range group.holders {
			terms = append(terms, `NOT (`+alias+`.run_id=`+q.param(holder.RunID)+` AND `+alias+`.node_id=`+q.param(holder.NodeID)+`)`)
		}
		positive := func(field string) string {
			value := q.number(q.at("capacity_group.value", field))
			return `CASE WHEN ` + value + `>0 THEN ` + value + ` ELSE 1 END`
		}
		match := q.at("capacity_group.value", "scope") + `=` + q.param(group.scope) + ` AND ` + q.at("capacity_group.value", "group") + `=` + q.param(group.group)
		match += ` AND COALESCE(` + q.at("capacity_group.value", "on_limit") + `,'') IN ('','queue') AND ` + positive("cost") + `<=` + positive("capacity")
		if !group.allDemands {
			match += ` AND ` + positive("capacity") + `=` + q.param(group.capacity) + ` AND ` + positive("cost") + `=` + q.param(group.cost)
		}
		terms = append(terms, `EXISTS (SELECT 1 FROM `+q.array(groups, "capacity_group")+` WHERE `+match+`)`)
		blocked = append(blocked, `(`+strings.Join(terms, ` AND `)+`)`)
	}
	return `(` + timed + ` OR NOT (` + strings.Join(blocked, ` OR `) + `))`
}
