package store

import (
	"context"
	"fmt"
	"slices"
	"time"
)

type schemaUpgrade struct {
	from       int
	release    string
	digest     string
	statements []string
}

// safety: only a store at a listed version whose digest matches that release is changed; every step runs inside
// the open transaction, which holds the migrate advisory lock on PostgreSQL, so concurrent openers upgrade once.
var schemaUpgrades = []schemaUpgrade{
	{
		from: 28, release: "v0.68.3", digest: "75de685ed349678b0fa36694978890a2ddd491851d7a63c4c6740e81408b166a",
		statements: []string{`ALTER TABLE nodes ADD COLUMN trusted_host BIGINT NOT NULL DEFAULT 0 CHECK(trusted_host IN (0,1))`},
	},
}

func schemaUpgradeFrom(version int) (schemaUpgrade, bool) {
	i := slices.IndexFunc(schemaUpgrades, func(u schemaUpgrade) bool { return u.from == version })
	if i < 0 {
		return schemaUpgrade{}, false
	}
	return schemaUpgrades[i], true
}

func upgradeSchemaTx(ctx context.Context, tx *storeTx, version int, path string) error {
	for version < expectedSchemaVersion {
		step, ok := schemaUpgradeFrom(version)
		if !ok {
			return schemaMismatchError(version, path)
		}
		recorded, err := recordedBaselineDigestTx(ctx, tx, version)
		if err != nil {
			return err
		}
		if recorded != step.digest {
			return schemaUpgradeDigestError(step, recorded, path)
		}
		for _, statement := range step.statements {
			translated, err := translateSchema(statement, tx.dialect)
			if err == nil {
				_, err = tx.execContext(ctx, translated)
			}
			if err != nil {
				return fmt.Errorf("upgrade store schema %d to %d: %w; the store is unchanged", version, version+1, err)
			}
		}
		next := schemaBaselineDigest()
		if version+1 < expectedSchemaVersion {
			following, ok := schemaUpgradeFrom(version + 1)
			if !ok {
				return schemaMismatchError(version+1, path)
			}
			next = following.digest
		}
		if _, err := tx.execContext(ctx, `UPDATE sparkwing_schema_version SET version = ?, applied_at = ?, baseline_digest = ? WHERE version = ?`,
			version+1, time.Now().UnixNano(), next, version); err != nil {
			return fmt.Errorf("record store schema %d: %w", version+1, err)
		}
		version++
	}
	return nil
}

// safety: a store without the digest column predates every release this binary upgrades, so it reads as missing.
func recordedBaselineDigestTx(ctx context.Context, tx *storeTx, version int) (string, error) {
	hasDigest, err := hasBaselineDigestColumnTx(ctx, tx)
	if err != nil || !hasDigest {
		return "", err
	}
	var recorded string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(baseline_digest, '') FROM sparkwing_schema_version WHERE version = ?`, version).Scan(&recorded); err != nil {
		return "", fmt.Errorf("read schema baseline digest: %w", err)
	}
	return recorded, nil
}
