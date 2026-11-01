package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

func schemaBaselineDigest() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.ReplaceAll(schemaNeutral, "\r\n", "\n"))))
}

func verifySchemaBaseline(ctx context.Context, tx *storeTx, path string) error {
	q := `SELECT COUNT(*) FROM pragma_table_info('sparkwing_schema_version') WHERE name = 'baseline_digest'`
	if tx.dialect == DialectPostgres {
		q = `SELECT COUNT(*) FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = 'sparkwing_schema_version' AND column_name = 'baseline_digest'`
	}
	var hasDigest int
	if err := tx.QueryRowContext(ctx, q).Scan(&hasDigest); err != nil {
		return fmt.Errorf("inspect schema digest column: %w", err)
	}
	var recorded string
	if hasDigest != 0 {
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(baseline_digest, '') FROM sparkwing_schema_version WHERE version = ?`, expectedSchemaVersion).Scan(&recorded); err != nil {
			return fmt.Errorf("read schema baseline digest: %w", err)
		}
	}
	expected := schemaBaselineDigest()
	if recorded == expected {
		return nil
	}
	if recorded != "" {
		return schemaBaselineMismatchError(recorded, expected, path)
	}
	if err := checkLegacySchemaObjects(ctx, tx); err != nil {
		return fmt.Errorf("%w; %w", schemaBaselineMismatchError("<missing>", expected, path), err)
	}
	if hasDigest == 0 {
		if _, err := tx.execContext(ctx, `ALTER TABLE sparkwing_schema_version ADD COLUMN baseline_digest TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add schema baseline digest: %w", err)
		}
	}
	_, err := tx.execContext(ctx, `UPDATE sparkwing_schema_version SET baseline_digest = ? WHERE version = ?`, expected, expectedSchemaVersion)
	return err
}

// safety: only explicitly declared objects are portable across catalogs; implicit indexes differ by dialect.
var baselineObject = regexp.MustCompile(`(?im)^CREATE\s+(?:UNIQUE\s+)?(TABLE|VIEW|INDEX)\s+([a-z_][a-z0-9_]*)\b`)

func checkLegacySchemaObjects(ctx context.Context, tx *storeTx) (err error) {
	q := `SELECT type, name FROM sqlite_master WHERE type IN ('table', 'view', 'index')`
	if tx.dialect == DialectPostgres {
		q = `SELECT CASE c.relkind WHEN 'r' THEN 'table' WHEN 'v' THEN 'view' WHEN 'i' THEN 'index' END, c.relname
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'v', 'i')`
	}
	rows, err := tx.queryContext(ctx, q)
	if err != nil {
		return err
	}
	defer closeRowsInto(rows, &err)
	objects := make(map[string]bool)
	for rows.Next() {
		var kind, name string
		if err := rows.Scan(&kind, &name); err != nil {
			return err
		}
		objects[kind+" "+name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, match := range baselineObject.FindAllStringSubmatch(schemaNeutral, -1) {
		object := strings.ToLower(match[1]) + " " + match[2]
		if !objects[object] {
			return fmt.Errorf("missing baseline object: %s", object)
		}
	}
	return nil
}
