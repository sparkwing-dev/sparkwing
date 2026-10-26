package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// PoolAccessRule is one canonical repository and branch selector persisted for a pool.
type PoolAccessRule struct {
	ID             string
	Pool           string
	RepositoryKey  string
	Branch         string
	Source         string
	Route          string
	InstallationID int64
}

// ReadPoolAccess reads this tenant's access selectors and server-granted reservations.
func (t *Tenant) ReadPoolAccess(ctx context.Context) ([]PoolAccessRule, map[string][]string, error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer rollbackOrLog(tx)
	rules, err := readPoolRulesTx(ctx, tx, t.team, "")
	if err != nil {
		return nil, nil, err
	}
	now := tx.now(ctx)
	rows, err := tx.queryContext(ctx, `SELECT prefix FROM capabilities WHERE team=? AND subject_kind='machine' AND bind_kind='none' AND parent_id IS NULL AND `+liveCredentialSQL("")+` ORDER BY subject_id,prefix`, t.team, now.UnixNano(), now.UnixNano())
	if err != nil {
		return nil, nil, err
	}
	var prefixes []string
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			rows.Close()
			return nil, nil, err
		}
		prefixes = append(prefixes, prefix)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, nil, err
	}
	runners := map[string][]string{}
	for _, prefix := range prefixes {
		c, err := loadCapabilityTx(ctx, tx, prefix)
		if err != nil {
			return nil, nil, err
		}
		for _, pool := range c.ReservedPools {
			if !slices.Contains(runners[pool], c.Subject.ID) {
				runners[pool] = append(runners[pool], c.Subject.ID)
			}
		}
	}
	return rules, runners, tx.Commit()
}

func readPoolRulesTx(ctx context.Context, tx *storeTx, team Team, id string) ([]PoolAccessRule, error) {
	query := `SELECT id,pool,repo_key,branch,source,route FROM pool_access_rules WHERE team=?`
	args := []any{team}
	if id != "" {
		query += ` AND (id=? OR substr(id,1,?)=?)`
		args = append(args, id, len(id)+1, id+".")
	}
	rows, err := tx.queryContext(ctx, query+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := []PoolAccessRule{}
	for rows.Next() {
		var rule PoolAccessRule
		if err := rows.Scan(&rule.ID, &rule.Pool, &rule.RepositoryKey, &rule.Branch, &rule.Source, &rule.Route); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

// WritePoolAccess atomically rechecks administration and bound installations before persisting selectors.
func (t *Tenant) WritePoolAccess(ctx context.Context, actor runstate.Actor, id string, rules []PoolAccessRule) (bool, error) {
	if id == "" || len(rules) == 0 {
		return false, authorityError(apierr.BadRequest, "pool rule requires selectors")
	}
	return inTxResult(ctx, t.s, func(tx *storeTx) (bool, error) {
		p, err := checkTeamAdministrationTx(ctx, tx, t.team, actor)
		if err != nil {
			return false, err
		}
		if err := advisoryLockTx(ctx, tx, "pool-rule:"+string(t.team)+":"+id); err != nil {
			return false, err
		}
		desired := make([]PoolAccessRule, len(rules))
		for i, rule := range rules {
			if rule.Pool == "" || !strings.HasPrefix(rule.RepositoryKey, "gh:") || rule.InstallationID <= 0 {
				return false, authorityError(apierr.BadRequest, "pool requires verified repository authority")
			}
			var found int64
			if err := tx.QueryRowContext(ctx, `SELECT installation_id FROM github_app_installations WHERE team=? AND installation_id=? AND suspended=0`+tx.forShare(), t.team, rule.InstallationID).Scan(&found); err != nil {
				return false, err
			}
			if rule.Branch != "" {
				selector, err := runspec.ParseBranchSelector([]string{rule.Branch})
				if err != nil {
					return false, authorityError(apierr.BadRequest, err.Error())
				}
				rule.Branch = selector[0]
				if strings.ContainsAny(rule.Branch, `[\`) {
					return false, authorityError(apierr.BadRequest, "pool rule branch patterns support only the * and ? wildcards")
				}
			}
			rule.ID = fmt.Sprintf("%s.%06d", id, i)
			rule.InstallationID = 0
			desired[i] = rule
		}
		existing, err := readPoolRulesTx(ctx, tx, t.team, id)
		if err != nil {
			return false, err
		}
		if len(existing) > 0 {
			if !reflect.DeepEqual(existing, desired) {
				return false, authorityError(apierr.Conflict, "idempotency key names another pool rule")
			}
			return true, nil
		}
		for _, rule := range desired {
			if _, err := tx.execContext(ctx, `INSERT INTO pool_access_rules(team,id,pool,repo_key,branch,source,route,created_by,created_at) VALUES (?,?,?,?,?,?,?,?,?)`, t.team, rule.ID, rule.Pool, rule.RepositoryKey, rule.Branch, rule.Source, rule.Route, p.Capability.Subject.ID, tx.now(ctx).UnixNano()); err != nil {
				return false, err
			}
		}
		return false, nil
	})
}

// DeletePoolAccess removes this pool's selected rule under current team administration.
func (t *Tenant) DeletePoolAccess(ctx context.Context, actor runstate.Actor, pool, id string) (bool, error) {
	if pool == "" || id == "" {
		return false, authorityError(apierr.BadRequest, "pool and rule are required")
	}
	return inTxResult(ctx, t.s, func(tx *storeTx) (bool, error) {
		if _, err := checkTeamAdministrationTx(ctx, tx, t.team, actor); err != nil {
			return false, err
		}
		result, err := tx.execContext(ctx, `DELETE FROM pool_access_rules WHERE team=? AND pool=? AND (id=? OR substr(id,1,?)=?)`, t.team, pool, id, len(id)+1, id+".")
		if err != nil {
			return false, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		return n > 0, nil
	})
}

// safety: the one pool access rule, read at every use (placement, secret release, OIDC), so
// removing a rule stops retries of older runs too. Branches are path.Match globs limited to
// * and ? by WritePoolAccess; an equal slash count keeps each wildcard in one path segment.
func (q *claimSQL) poolGrant(rule, provenance string) string {
	trust, ref := q.at(provenance, "trust"), `COALESCE(`+q.at(provenance, "ref")+`,'')`
	branch := `substr(` + ref + `,12)`
	glob := branch + ` GLOB ` + rule + `.branch`
	if q.dialect == DialectPostgres {
		glob = branch + ` LIKE replace(replace(replace(replace(replace(` + rule + `.branch,'\','\\'),'%','\%'),'_','\_'),'*','%'),'?','_') ESCAPE '\'`
	}
	slashes := func(v string) string { return `(length(` + v + `)-length(replace(` + v + `,'/','')))` }
	return `(` + trust + ` IN ('verified','verified-default','schedule') AND ` + rule + `.repo_key='gh:'||CAST(` + q.at(provenance, "repository_id") + ` AS TEXT)` +
		` AND (` + rule + `.source='' OR (` + rule + `.source='github_app' AND ` + trust + ` IN ('verified','verified-default')))` +
		` AND (` + rule + `.branch='' OR (substr(` + ref + `,1,11)='refs/heads/' AND ` + slashes(rule+".branch") + `=` + slashes(branch) + ` AND ` + glob + `)))`
}

func (q *claimSQL) allowedPools(team, provenance string) string {
	from := ` FROM pool_access_rules pr WHERE pr.team=` + team + ` AND ` + q.poolGrant("pr", provenance)
	if q.dialect == DialectPostgres {
		return `(SELECT COALESCE(json_agg(DISTINCT pr.pool),'[]'::json)::text` + from + `)`
	}
	return `(SELECT json_group_array(pool) FROM (SELECT DISTINCT pr.pool` + from + `))`
}

func runPoolsTx(ctx context.Context, tx *storeTx, team Team, provenance runspec.Provenance) (pools, routed []string, err error) {
	body, err := json.Marshal(provenance)
	if err != nil {
		return nil, nil, err
	}
	q := claimSQL{dialect: tx.dialect}
	rows, err := tx.queryContext(ctx, `SELECT pr.pool,MAX(pr.route) FROM pool_access_rules pr CROSS JOIN (SELECT CAST(? AS TEXT) AS provenance) v WHERE pr.team=? AND `+q.poolGrant("pr", "v.provenance")+` GROUP BY pr.pool`, string(body), team)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	pools = []string{}
	for rows.Next() {
		var pool, route string
		if err := rows.Scan(&pool, &route); err != nil {
			return nil, nil, err
		}
		pools = append(pools, pool)
		if route != "" {
			routed = append(routed, pool)
		}
	}
	slices.Sort(pools)
	return pools, routed, rows.Err()
}
