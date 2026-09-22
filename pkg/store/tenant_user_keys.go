package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// safety: each entry rebuilds one table's primary key to lead with the
// team, and the copy carries every existing row through in the same
// statement, because several lookups on these tables answer a miss with
// a default and a version between widening and backfill reads as unset.
var teamScopedUserKeys = []teamScopedKey{
	{
		table: "secrets",
		key:   []string{"team", "name", "pipeline"},
	},
	{
		table: "github_webhook_bindings",
		key:   []string{"team", "pipeline", "repo"},
	},
	{
		table: "pipeline_profiles",
		key:   []string{"team", "pipeline", "node_id"},
	},
	{
		table: "concurrency_entries",
		key:   []string{"team", "key"},
	},
	{
		table: "concurrency_holders",
		key:   []string{"team", "key", "holder_id"},
		// safety: every read of this index now names a team, so an index
		// still leading with the concurrency key would scan every team's
		// holders of a key shared across teams.
		replaceIndexes: map[string]string{
			"idx_concurrency_holders_key_claimed": `CREATE INDEX IF NOT EXISTS idx_concurrency_holders_key_claimed
    ON concurrency_holders(team, key, claimed_at)`,
		},
	},
	{
		table: "concurrency_waiters",
		key:   []string{"team", "key", "run_id", "node_id"},
		replaceIndexes: map[string]string{
			"idx_concurrency_waiters_arrived": `CREATE INDEX IF NOT EXISTS idx_concurrency_waiters_arrived
    ON concurrency_waiters(team, key, arrived_at)`,
		},
	},
	{
		table: "concurrency_cache",
		key:   []string{"team", "key", "cache_key_hash"},
	},
}

type teamScopedKey struct {
	table string
	key   []string
	// safety: keyed by the name of an index the table already carries,
	// valued by the definition that replaces it.
	replaceIndexes map[string]string
}

func applyUserKeyTeamScopeMigrationSQLite(ctx context.Context, tx *storeTx) error {
	for _, spec := range teamScopedUserKeys {
		if err := widenPrimaryKeySQLite(ctx, tx, spec); err != nil {
			return fmt.Errorf("widen %s primary key: %w", spec.table, err)
		}
	}
	return nil
}

func applyUserKeyTeamScopeMigrationPostgres(ctx context.Context, tx *storeTx) error {
	for _, spec := range teamScopedUserKeys {
		if err := widenPrimaryKeyPostgres(ctx, tx, spec); err != nil {
			return fmt.Errorf("widen %s primary key: %w", spec.table, err)
		}
	}
	return nil
}

// safety: SQLite cannot alter a primary key in place, so widening one
// rebuilds the table. The columns, types, null-ness and defaults come off
// the live table because the ladder reaches this version by several
// routes and a hand-copied shape would drift from one of them.
func widenPrimaryKeySQLite(ctx context.Context, tx *storeTx, spec teamScopedKey) error {
	cols, err := sqliteColumnsOf(ctx, tx, spec.table)
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return fmt.Errorf("table %s has no columns", spec.table)
	}
	for _, name := range spec.key {
		if !hasColumn(cols, name) {
			return fmt.Errorf("table %s has no column %s to key on", spec.table, name)
		}
	}
	indexes, err := sqliteIndexesOf(ctx, tx, spec.table)
	if err != nil {
		return err
	}

	rebuilt := spec.table + "__team_keyed"
	defs := make([]string, 0, len(cols)+1)
	names := make([]string, 0, len(cols))
	for _, c := range cols {
		defs = append(defs, c.definition())
		names = append(names, quoteIdent(c.name))
	}
	defs = append(defs, "PRIMARY KEY ("+strings.Join(quoteIdents(spec.key), ", ")+")")
	list := strings.Join(names, ", ")

	stmts := []string{
		`DROP TABLE IF EXISTS ` + quoteIdent(rebuilt),
		`CREATE TABLE ` + quoteIdent(rebuilt) + ` (` + strings.Join(defs, ", ") + `)`,
		`INSERT INTO ` + quoteIdent(rebuilt) + ` (` + list + `) SELECT ` + list + ` FROM ` + quoteIdent(spec.table),
		`DROP TABLE ` + quoteIdent(spec.table),
		`ALTER TABLE ` + quoteIdent(rebuilt) + ` RENAME TO ` + quoteIdent(spec.table),
	}
	for _, idx := range indexes {
		if replacement, ok := spec.replaceIndexes[idx.name]; ok {
			stmts = append(stmts, replacement)
			continue
		}
		stmts = append(stmts, idx.ddl)
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", collapseSQL(stmt), err)
		}
	}
	return nil
}

func widenPrimaryKeyPostgres(ctx context.Context, tx *storeTx, spec teamScopedKey) error {
	current, err := postgresPrimaryKeyName(ctx, tx, spec.table)
	if err != nil {
		return err
	}
	var stmts []string
	if current != "" {
		stmts = append(stmts, `ALTER TABLE `+quoteIdent(spec.table)+` DROP CONSTRAINT `+quoteIdent(current))
	}
	stmts = append(stmts,
		`ALTER TABLE `+quoteIdent(spec.table)+` ADD PRIMARY KEY (`+strings.Join(quoteIdents(spec.key), ", ")+`)`)
	// safety: Postgres keeps an index across a primary key swap, so the
	// ones that lead with the user's key are dropped by name and remade.
	for name, replacement := range spec.replaceIndexes {
		stmts = append(stmts, `DROP INDEX IF EXISTS `+quoteIdent(name), replacement)
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", collapseSQL(stmt), err)
		}
	}
	return nil
}

func postgresPrimaryKeyName(ctx context.Context, tx *storeTx, table string) (string, error) {
	var name string
	err := tx.QueryRowContext(ctx, `
        SELECT c.conname
          FROM pg_constraint c
          JOIN pg_class t ON t.oid = c.conrelid
          JOIN pg_namespace n ON n.oid = t.relnamespace
         WHERE c.contype = 'p' AND t.relname = ? AND n.nspname = current_schema()`, table).Scan(&name)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return name, nil
}

type sqliteColumn struct {
	name    string
	typ     string
	notNull bool
	dflt    sql.NullString
}

func (c sqliteColumn) definition() string {
	def := quoteIdent(c.name)
	if c.typ != "" {
		def += " " + c.typ
	}
	if c.notNull {
		def += " NOT NULL"
	}
	if c.dflt.Valid {
		def += " DEFAULT " + c.dflt.String
	}
	return def
}

func sqliteColumnsOf(ctx context.Context, tx *storeTx, table string) (_ []sqliteColumn, err error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []sqliteColumn
	for rows.Next() {
		var (
			cid          int
			col          sqliteColumn
			notNull, pk  int
			defaultValue sql.NullString
		)
		if err := rows.Scan(&cid, &col.name, &col.typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		col.notNull = notNull != 0
		col.dflt = defaultValue
		out = append(out, col)
	}
	return out, rows.Err()
}

type sqliteIndex struct {
	name string
	ddl  string
}

// safety: SQLite drops a table's indexes with the table, so the rebuild
// replays their definitions; the implicit index behind a primary key has
// a NULL definition and is left out, because the new key creates its own.
func sqliteIndexesOf(ctx context.Context, tx *storeTx, table string) (_ []sqliteIndex, err error) {
	rows, err := tx.QueryContext(ctx, `
        SELECT name, sql FROM sqlite_master
         WHERE type = 'index' AND tbl_name = ? AND sql IS NOT NULL
         ORDER BY name`, table)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []sqliteIndex
	for rows.Next() {
		var idx sqliteIndex
		if err := rows.Scan(&idx.name, &idx.ddl); err != nil {
			return nil, err
		}
		out = append(out, idx)
	}
	return out, rows.Err()
}

func hasColumn(cols []sqliteColumn, name string) bool {
	for _, c := range cols {
		if c.name == name {
			return true
		}
	}
	return false
}

func quoteIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func quoteIdents(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, quoteIdent(name))
	}
	return out
}

func collapseSQL(s string) string { return strings.Join(strings.Fields(s), " ") }
