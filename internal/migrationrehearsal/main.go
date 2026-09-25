// Command migrationrehearsal is the data half of bin/rehearse-migration.sh. It
// snapshots a live SQLite store, plants canary credentials in a copy, records
// what a store holds, compares a migrated copy against its baseline, and
// checks a controller started on the migrated copy through its HTTP API.
//
// It reads stores with raw SQL rather than through pkg/store, so the build
// under test is never the thing judging itself.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
	_ "modernc.org/sqlite"
)

const (
	canaryPrincipal = "rehearsal-canary"
	canaryUser      = "rehearsal-canary"
	tokenPrefixLen  = 12
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "snapshot":
		err = runSnapshot(os.Args[2:])
	case "canary":
		err = runCanary(os.Args[2:])
	case "inventory":
		err = runInventory(os.Args[2:])
	case "compare":
		err = runCompare(os.Args[2:])
	case "api-check":
		err = runAPICheck(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrationrehearsal:", err)
		os.Exit(1)
	}
}

func closeLogged(what string, c io.Closer) {
	if err := c.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "migrationrehearsal: close %s: %v\n", what, err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: migrationrehearsal <command> [flags]

  snapshot  -from <state.db> -to <copy.db>
  canary    -dialect sqlite|postgres -dsn <dsn> -out <dir>
  inventory -dialect sqlite|postgres -dsn <dsn>
  compare   -dialect sqlite|postgres -before <dsn> -after <dsn> -want-version <n>
  api-check -url <controller> -creds <dir> -inventory <before.json> [-keyed]`)
	os.Exit(2)
}

type db struct {
	*sql.DB
	dialect string
}

func open(dialect, dsn string) (*db, error) {
	driver := map[string]string{"sqlite": "sqlite", "postgres": "pgx"}[dialect]
	if driver == "" {
		return nil, fmt.Errorf("dialect %q is neither sqlite nor postgres", dialect)
	}
	if dialect == "sqlite" {
		dsn = "file:" + dsn + "?_pragma=busy_timeout(30000)"
	}
	d, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	if err := d.Ping(); err != nil {
		return nil, err
	}
	return &db{DB: d, dialect: dialect}, nil
}

func (d *db) bind(q string) string {
	if d.dialect != "postgres" {
		return q
	}
	var b strings.Builder
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func quote(ident string) string { return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"` }

// safety: VACUUM INTO reads one consistent snapshot through SQLite's own
// locking, so a daemon writing the store mid-copy cannot tear it the way a
// file copy of state.db and its -wal can.
func runSnapshot(args []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	from := fs.String("from", "", "live SQLite store to read")
	to := fs.String("to", "", "path for the consistent copy; must not exist")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" || *to == "" {
		return errors.New("snapshot needs -from and -to")
	}
	if _, err := os.Stat(*to); err == nil {
		return fmt.Errorf("%s already exists", *to)
	}
	src, err := sql.Open("sqlite", "file:"+*from+"?mode=ro&_pragma=busy_timeout(30000)")
	if err != nil {
		return err
	}
	defer closeLogged("source store", src)
	start := time.Now()
	if _, err := src.Exec(`VACUUM INTO ?`, *to); err != nil {
		return fmt.Errorf("vacuum into %s: %w", *to, err)
	}
	if err := os.Chmod(*to, 0o600); err != nil {
		return err
	}
	st, err := os.Stat(*to)
	if err != nil {
		return err
	}
	fmt.Printf("snapshot %s -> %s: %d bytes in %s\n", *from, *to, st.Size(), time.Since(start).Round(time.Millisecond))
	return nil
}

// safety: tokens and passwords are stored only as argon2id hashes, so the one
// way to prove a pre-upgrade credential still authenticates afterwards is to
// plant one whose raw value is known. The parameters match pkg/store.
func argonHash(secret string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(secret), salt, 1, 64*1024, 4, 32)
	return fmt.Sprintf("argon2id$%s$%s", hex.EncodeToString(salt), hex.EncodeToString(key)), nil
}

func randomText(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func runCanary(args []string) error {
	fs := flag.NewFlagSet("canary", flag.ExitOnError)
	dialect := fs.String("dialect", "", "sqlite or postgres")
	dsn := fs.String("dsn", "", "store to plant the canaries in")
	out := fs.String("out", "", "directory for the canary token and password")
	if err := fs.Parse(args); err != nil {
		return err
	}
	d, err := open(*dialect, *dsn)
	if err != nil {
		return err
	}
	defer closeLogged("store", d)
	body, err := randomText(32)
	if err != nil {
		return err
	}
	raw := "swu_" + body
	password, err := randomText(18)
	if err != nil {
		return err
	}
	tokHash, err := argonHash(raw)
	if err != nil {
		return err
	}
	pwHash, err := argonHash(password)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	if err := d.insertKnown("tokens", map[string]any{
		"hash": tokHash, "prefix": raw[:tokenPrefixLen], "principal": canaryPrincipal,
		"kind": "user", "scopes": "admin", "created_at": now,
	}); err != nil {
		return err
	}
	if err := d.insertKnown("users", map[string]any{
		"name": canaryUser, "pw_hash": pwHash, "created_at": now, "scopes": "admin",
	}); err != nil {
		return err
	}
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	digests, err := d.plantSecrets(key, now)
	if err != nil {
		return err
	}
	digestJSON, err := json.Marshal(digests)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o700); err != nil {
		return err
	}
	for name, v := range map[string]string{
		"canary-token": raw, "canary-user": canaryUser, "canary-password": password,
		"secrets-key":         base64.StdEncoding.EncodeToString(key),
		"canary-secrets.json": string(digestJSON),
	} {
		if err := os.WriteFile(filepath.Join(*out, name), []byte(v), 0o600); err != nil {
			return err
		}
	}
	fmt.Printf("canary token %s, password user %s and %d secrets (plaintext, enc:v1, enc:v2) planted\n",
		raw[:tokenPrefixLen], canaryUser, len(digests))
	return nil
}

// safety: a store written by an earlier release can hold plaintext rows, v1
// envelopes bound to nothing and v2 envelopes bound to their row without a
// team; the upgrade reseals all three, so the canaries cover all three.
func (d *db) plantSecrets(key []byte, now int64) (map[string]string, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	seal := func(prefix, plain string, aad []byte) (string, error) {
		nonce := make([]byte, aead.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			return "", err
		}
		return prefix + base64.StdEncoding.EncodeToString(append(nonce, aead.Seal(nil, nonce, []byte(plain), aad)...)), nil
	}
	canaries := []struct {
		name, pipeline, prefix string
		shared                 bool
	}{
		{"REHEARSAL_PLAINTEXT", "", "", true},
		{"REHEARSAL_V1", "", "enc:v1:", true},
		{"REHEARSAL_V2", "rehearsal-pipeline", "enc:v2:", false},
	}
	digests := map[string]string{}
	for _, cn := range canaries {
		plain, err := randomText(24)
		if err != nil {
			return nil, err
		}
		value := plain
		switch cn.prefix {
		case "enc:v1:":
			value, err = seal(cn.prefix, plain, nil)
		case "enc:v2:":
			value, err = seal(cn.prefix, plain, legacyAAD(cn.name, cn.pipeline, cn.shared, true))
		}
		if err != nil {
			return nil, err
		}
		if err := d.insertKnown("secrets", map[string]any{
			"name": cn.name, "value": value, "principal": canaryPrincipal,
			"created_at": now, "updated_at": now, "masked": 1, "shared": boolInt(cn.shared),
			"pipeline": cn.pipeline, "repo": cn.pipeline,
		}); err != nil {
			return nil, err
		}
		sum := sha256.Sum256([]byte(plain))
		digests[cn.name+"\x1f"+cn.pipeline] = hex.EncodeToString(sum[:])
	}
	return digests, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func legacyAAD(name, scope string, shared, masked bool) []byte {
	field := func(dst []byte, f string) []byte {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		return append(append(dst, n[:]...), f...)
	}
	aad := field(field(nil, name), scope)
	return append(aad, byte(boolInt(shared)), byte(boolInt(masked)))
}

type column struct {
	name       string
	notNull    bool
	hasDefault bool
	pk         int
}

func (d *db) columns(table string) ([]column, error) {
	var rows *sql.Rows
	var err error
	if d.dialect == "sqlite" {
		rows, err = d.Query(`SELECT name, "notnull", dflt_value IS NOT NULL, pk FROM pragma_table_info(?)`, table)
	} else {
		rows, err = d.Query(`
			SELECT c.column_name, c.is_nullable = 'NO', c.column_default IS NOT NULL,
			       COALESCE((SELECT k.ord
			                   FROM pg_index i
			                   CROSS JOIN LATERAL unnest(i.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
			                   JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
			                  WHERE i.indrelid = to_regclass(quote_ident(c.table_name)) AND i.indisprimary
			                    AND a.attname = c.column_name), 0)
			  FROM information_schema.columns c
			 WHERE c.table_schema = current_schema() AND c.table_name = $1
			 ORDER BY c.ordinal_position`, table)
	}
	if err != nil {
		return nil, err
	}
	defer closeLogged("rows", rows)
	var out []column
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.name, &c.notNull, &c.hasDefault, &c.pk); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// safety: the canary is written against the pre-upgrade schema, so a
// required column this tool does not know means the schema moved under it and
// the insert would plant a row the old binary could never have written.
func (d *db) insertKnown(table string, values map[string]any) error {
	cols, err := d.columns(table)
	if err != nil {
		return err
	}
	var names, marks []string
	var args []any
	for _, c := range cols {
		v, ok := values[c.name]
		if !ok {
			if c.notNull && !c.hasDefault {
				return fmt.Errorf("%s.%s is required and the canary does not know it", table, c.name)
			}
			continue
		}
		names = append(names, quote(c.name))
		marks = append(marks, "?")
		args = append(args, v)
	}
	_, err = d.Exec(d.bind(fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`,
		quote(table), strings.Join(names, ", "), strings.Join(marks, ", "))), args...)
	if err != nil {
		return fmt.Errorf("plant canary in %s: %w", table, err)
	}
	return nil
}

type schemaVersion struct {
	Version   int   `json:"version"`
	AppliedAt int64 `json:"applied_at"`
}

type lastRun struct {
	ID        string `json:"id"`
	CreatedAt int64  `json:"created_at"`
	Nodes     int64  `json:"nodes"`
}

type secretRow struct {
	Name     string `json:"name"`
	Pipeline string `json:"pipeline"`
	Team     string `json:"team,omitempty"`
	Envelope string `json:"envelope"`
	Digest   string `json:"sha256_if_plaintext,omitempty"`
}

type inventory struct {
	Dialect        string                      `json:"dialect"`
	SchemaVersion  int                         `json:"schema_version"`
	Versions       []schemaVersion             `json:"versions"`
	Requirements   []string                    `json:"requirements"`
	Tables         map[string]int64            `json:"tables"`
	Runs           int64                       `json:"runs"`
	Nodes          int64                       `json:"nodes"`
	Tokens         int64                       `json:"tokens"`
	Secrets        int64                       `json:"secrets"`
	Users          int64                       `json:"users"`
	LastRun        *lastRun                    `json:"last_run,omitempty"`
	TokenPrefixes  []string                    `json:"token_prefixes"`
	SecretRows     []secretRow                 `json:"secret_rows"`
	EnvelopeCounts map[string]int              `json:"secret_envelopes"`
	Teams          map[string]map[string]int64 `json:"teams"`
}

func (d *db) tables() ([]string, error) {
	q := `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`
	if d.dialect == "postgres" {
		q = `SELECT table_name FROM information_schema.tables
		      WHERE table_schema = current_schema() AND table_type = 'BASE TABLE' ORDER BY table_name`
	}
	return d.strings(q)
}

func (d *db) strings(q string, args ...any) ([]string, error) {
	rows, err := d.Query(d.bind(q), args...)
	if err != nil {
		return nil, err
	}
	defer closeLogged("rows", rows)
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *db) count(table string) (int64, error) {
	var n int64
	err := d.QueryRow(`SELECT COUNT(*) FROM ` + quote(table)).Scan(&n)
	return n, err
}

func envelopeClass(v string) string {
	for _, p := range []string{"enc:v1:", "enc:v2:", "enc:v3:"} {
		if strings.HasPrefix(v, p) {
			return strings.TrimSuffix(p, ":")
		}
	}
	if strings.HasPrefix(v, "enc:") {
		return "enc:other"
	}
	return "plaintext"
}

func takeInventory(d *db) (*inventory, error) {
	inv := &inventory{Dialect: d.dialect, Tables: map[string]int64{}, EnvelopeCounts: map[string]int{}, Teams: map[string]map[string]int64{}}
	names, err := d.tables()
	if err != nil {
		return nil, err
	}
	for _, t := range names {
		n, err := d.count(t)
		if err != nil {
			return nil, fmt.Errorf("count %s: %w", t, err)
		}
		inv.Tables[t] = n
		cols, err := d.columns(t)
		if err != nil {
			return nil, err
		}
		for _, c := range cols {
			if c.name != "team" {
				continue
			}
			byTeam := map[string]int64{}
			rows, err := d.Query(`SELECT team, COUNT(*) FROM ` + quote(t) + ` GROUP BY team`)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var team sql.NullString
				var n int64
				if err := rows.Scan(&team, &n); err != nil {
					closeLogged("rows", rows)
					return nil, err
				}
				key := team.String
				if !team.Valid {
					key = "<null>"
				}
				byTeam[key] = n
			}
			closeLogged("rows", rows)
			inv.Teams[t] = byTeam
		}
	}
	inv.Runs, inv.Nodes, inv.Tokens, inv.Secrets, inv.Users = inv.Tables["runs"], inv.Tables["nodes"], inv.Tables["tokens"], inv.Tables["secrets"], inv.Tables["users"]
	rows, err := d.Query(`SELECT version, applied_at FROM sparkwing_schema_version ORDER BY version`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v schemaVersion
		if err := rows.Scan(&v.Version, &v.AppliedAt); err != nil {
			closeLogged("rows", rows)
			return nil, err
		}
		inv.Versions = append(inv.Versions, v)
		inv.SchemaVersion = max(inv.SchemaVersion, v.Version)
	}
	closeLogged("rows", rows)
	if _, ok := inv.Tables["sparkwing_requirements"]; ok {
		if inv.Requirements, err = d.strings(`SELECT name FROM sparkwing_requirements ORDER BY name`); err != nil {
			return nil, err
		}
	}
	if inv.Runs > 0 {
		var lr lastRun
		if err := d.QueryRow(`SELECT id, created_at FROM runs ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&lr.ID, &lr.CreatedAt); err != nil {
			return nil, err
		}
		if err := d.QueryRow(d.bind(`SELECT COUNT(*) FROM nodes WHERE run_id = ?`), lr.ID).Scan(&lr.Nodes); err != nil {
			return nil, err
		}
		inv.LastRun = &lr
	}
	if inv.TokenPrefixes, err = d.strings(`SELECT prefix FROM tokens ORDER BY prefix`); err != nil {
		return nil, err
	}
	secretCols, err := d.columns("secrets")
	if err != nil {
		return nil, err
	}
	teamExpr, pipelineCol := "''", "pipeline"
	for _, c := range secretCols {
		if c.name == "team" {
			teamExpr = "team"
		}
		if c.name == "repo" {
			pipelineCol = "repo"
		}
	}
	srows, err := d.Query(`SELECT name, ` + pipelineCol + `, ` + teamExpr + `, value FROM secrets ORDER BY name, ` + pipelineCol)
	if err != nil {
		return nil, err
	}
	defer closeLogged("secret rows", srows)
	for srows.Next() {
		var s secretRow
		var value string
		if err := srows.Scan(&s.Name, &s.Pipeline, &s.Team, &value); err != nil {
			return nil, err
		}
		s.Envelope = envelopeClass(value)
		if s.Envelope == "plaintext" {
			sum := sha256.Sum256([]byte(value))
			s.Digest = hex.EncodeToString(sum[:])
		}
		inv.EnvelopeCounts[s.Envelope]++
		inv.SecretRows = append(inv.SecretRows, s)
	}
	return inv, srows.Err()
}

func runInventory(args []string) error {
	fs := flag.NewFlagSet("inventory", flag.ExitOnError)
	dialect := fs.String("dialect", "", "sqlite or postgres")
	dsn := fs.String("dsn", "", "store to read")
	if err := fs.Parse(args); err != nil {
		return err
	}
	d, err := open(*dialect, *dsn)
	if err != nil {
		return err
	}
	defer closeLogged("store", d)
	inv, err := takeInventory(d)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(inv)
}

type checker struct {
	failures int
}

func (c *checker) pass(format string, a ...any) { fmt.Printf("PASS  "+format+"\n", a...) }
func (c *checker) note(format string, a ...any) { fmt.Printf("NOTE  "+format+"\n", a...) }
func (c *checker) fail(format string, a ...any) {
	c.failures++
	fmt.Printf("FAIL  "+format+"\n", a...)
}

func (c *checker) err() error {
	if c.failures > 0 {
		return fmt.Errorf("%d check(s) failed", c.failures)
	}
	return nil
}

func (d *db) keySet(table string, cols []string) (map[string]struct{}, error) {
	exprs := make([]string, len(cols))
	for i, c := range cols {
		if d.dialect == "postgres" {
			exprs[i] = "COALESCE(" + quote(c) + "::text, '<null>')"
		} else {
			exprs[i] = "COALESCE(CAST(" + quote(c) + " AS TEXT), '<null>')"
		}
	}
	rows, err := d.Query(`SELECT ` + strings.Join(exprs, ", ") + ` FROM ` + quote(table))
	if err != nil {
		return nil, err
	}
	defer closeLogged("rows", rows)
	out := map[string]struct{}{}
	vals := make([]string, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		out[strings.Join(vals, "\x1f")] = struct{}{}
	}
	return out, rows.Err()
}

func runCompare(args []string) error {
	fs := flag.NewFlagSet("compare", flag.ExitOnError)
	dialect := fs.String("dialect", "", "sqlite or postgres")
	beforeDSN := fs.String("before", "", "the untouched baseline copy")
	afterDSN := fs.String("after", "", "the migrated copy")
	want := fs.Int("want-version", 0, "schema version the migration must reach")
	if err := fs.Parse(args); err != nil {
		return err
	}
	before, err := open(*dialect, *beforeDSN)
	if err != nil {
		return fmt.Errorf("open baseline: %w", err)
	}
	defer closeLogged("baseline store", before)
	after, err := open(*dialect, *afterDSN)
	if err != nil {
		return fmt.Errorf("open migrated: %w", err)
	}
	defer closeLogged("migrated store", after)
	bi, err := takeInventory(before)
	if err != nil {
		return err
	}
	ai, err := takeInventory(after)
	if err != nil {
		return err
	}
	var c checker
	if ai.SchemaVersion == *want {
		c.pass("schema version %d -> %d", bi.SchemaVersion, ai.SchemaVersion)
	} else {
		c.fail("schema version is %d, want %d", ai.SchemaVersion, *want)
	}
	for _, v := range ai.Versions {
		if v.Version > bi.SchemaVersion {
			fmt.Printf("      v%d applied at %s\n", v.Version, time.Unix(0, v.AppliedAt).UTC().Format(time.RFC3339Nano))
		}
	}
	c.note("requirements after: %s", strings.Join(ai.Requirements, ", "))

	tables := make([]string, 0, len(bi.Tables))
	for t := range bi.Tables {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, t := range tables {
		bn := bi.Tables[t]
		an, ok := ai.Tables[t]
		if !ok {
			if bn == 0 {
				c.note("%s: dropped by the migration, was empty", t)
			} else {
				c.fail("%s: dropped by the migration with %d rows", t, bn)
			}
			continue
		}
		cols, err := before.columns(t)
		if err != nil {
			return err
		}
		var pk []string
		sort.SliceStable(cols, func(i, j int) bool { return cols[i].pk < cols[j].pk })
		for _, col := range cols {
			if col.pk > 0 {
				pk = append(pk, col.name)
			}
		}
		if len(pk) == 0 {
			for _, col := range cols {
				pk = append(pk, col.name)
			}
		}
		if bn == 0 {
			if an != 0 {
				c.note("%s: 0 -> %d rows", t, an)
			}
			continue
		}
		bk, err := before.keySet(t, pk)
		if err != nil {
			return fmt.Errorf("%s keys before: %w", t, err)
		}
		ak, err := after.keySet(t, renamed(t, pk))
		if err != nil {
			c.fail("%s: cannot read the baseline key (%s) back from the migrated copy: %v", t, strings.Join(pk, ","), err)
			continue
		}
		missing := 0
		for k := range bk {
			if _, ok := ak[k]; !ok {
				missing++
			}
		}
		switch {
		case missing > 0:
			c.fail("%s: %d of %d rows (by %s) missing after migration", t, missing, len(bk), strings.Join(pk, ","))
		case an != bn:
			c.note("%s: every baseline row present; %d -> %d rows", t, bn, an)
		default:
			c.pass("%s: %d rows, every one present by %s", t, an, strings.Join(pk, ","))
		}
	}
	for _, t := range sortedKeys(ai.Tables) {
		if _, ok := bi.Tables[t]; !ok {
			c.note("%s: new table, %d rows", t, ai.Tables[t])
		}
	}
	for _, t := range sortedKeys(ai.Teams) {
		for team, n := range ai.Teams[t] {
			if team != "default" && n > 0 {
				c.fail("%s: %d rows carry team %q, want every row in default", t, n, team)
			}
		}
	}
	c.pass("team column checked on %d tables", len(ai.Teams))
	if strings.Join(bi.TokenPrefixes, ",") == strings.Join(ai.TokenPrefixes, ",") {
		c.pass("token prefixes unchanged (%d)", len(ai.TokenPrefixes))
	} else {
		c.fail("token prefixes changed: before %v after %v", bi.TokenPrefixes, ai.TokenPrefixes)
	}
	if bi.LastRun != nil && ai.LastRun != nil && *bi.LastRun == *ai.LastRun {
		c.pass("last run %s unchanged, %d nodes", ai.LastRun.ID, ai.LastRun.Nodes)
	} else if bi.LastRun != nil {
		c.fail("last run changed: before %+v after %+v", bi.LastRun, ai.LastRun)
	}
	c.note("secret envelopes before %v after %v", bi.EnvelopeCounts, ai.EnvelopeCounts)
	return c.err()
}

// safety: v48 renamed these key-bearing columns, so a baseline read before it
// names columns the migrated copy no longer has.
var columnRenames = map[string]map[string]string{
	"runs":    {"repo": "declared_repo"},
	"secrets": {"repo": "pipeline"},
}

func renamed(table string, cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c
		if to, ok := columnRenames[table][c]; ok {
			out[i] = to
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type client struct {
	base string
	auth string
}

func (cl client) do(method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, cl.base+path, rd)
	if err != nil {
		return 0, err
	}
	if cl.auth != "" {
		req.Header.Set("Authorization", cl.auth)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: decode: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

func readCred(dir, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	return strings.TrimSpace(string(b)), err
}

// safety: reads only, apart from the one session the password login creates,
// so the check can run against any copy without changing what it verifies.
func runAPICheck(args []string) error {
	fs := flag.NewFlagSet("api-check", flag.ExitOnError)
	base := fs.String("url", "", "controller started on the migrated copy")
	creds := fs.String("creds", "", "directory canary wrote")
	invPath := fs.String("inventory", "", "baseline inventory JSON")
	keyed := fs.Bool("keyed", false, "the controller runs with a secrets key, so every secret must be a v3 envelope that decrypts to its baseline value")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := os.ReadFile(*invPath)
	if err != nil {
		return err
	}
	var inv inventory
	if err := json.Unmarshal(raw, &inv); err != nil {
		return err
	}
	token, err := readCred(*creds, "canary-token")
	if err != nil {
		return err
	}
	user, err := readCred(*creds, "canary-user")
	if err != nil {
		return err
	}
	password, err := readCred(*creds, "canary-password")
	if err != nil {
		return err
	}
	var c checker
	anon := client{base: *base}
	bearer := client{base: *base, auth: "Bearer " + token}

	var caps struct {
		Teams struct {
			Enabled bool `json:"enabled"`
		} `json:"teams"`
	}
	if _, err := anon.do("GET", "/api/v1/capabilities", nil, &caps); err != nil {
		c.fail("capabilities: %v", err)
	} else if caps.Teams.Enabled {
		c.fail("capabilities report teams enabled without a license")
	} else {
		c.pass("capabilities: single-tenant (teams.enabled=false)")
	}

	var who map[string]any
	if _, err := bearer.do("GET", "/api/v1/auth/whoami", nil, &who); err != nil {
		c.fail("canary token (planted before the upgrade) does not authenticate: %v", err)
	} else {
		c.pass("canary token authenticates: whoami %v", who)
	}
	if code, err := anon.do("GET", "/api/v1/runs?limit=1", nil, nil); err == nil {
		c.fail("unauthenticated runs list answered %d; a store holding tokens must require one", code)
	} else {
		c.pass("unauthenticated runs list refused (%d)", code)
	}

	var runs struct {
		Runs []map[string]any `json:"runs"`
	}
	if _, err := bearer.do("GET", "/api/v1/runs?limit=500", nil, &runs); err != nil {
		c.fail("runs list: %v", err)
	} else {
		c.pass("runs list: %d runs returned (store holds %d)", len(runs.Runs), inv.Runs)
		if inv.LastRun != nil {
			found := false
			for _, r := range runs.Runs {
				if r["id"] == inv.LastRun.ID {
					found = true
				}
			}
			if found {
				c.pass("runs list includes the last run %s", inv.LastRun.ID)
			} else {
				c.fail("runs list does not include the last run %s", inv.LastRun.ID)
			}
		}
	}
	if inv.LastRun != nil {
		var run map[string]any
		if _, err := bearer.do("GET", "/api/v1/runs/"+url.PathEscape(inv.LastRun.ID), nil, &run); err != nil {
			c.fail("runs get %s: %v", inv.LastRun.ID, err)
		} else {
			c.pass("runs get %s: status %v pipeline %v", inv.LastRun.ID, run["status"], run["pipeline"])
		}
		var nodes struct {
			Nodes []map[string]any `json:"nodes"`
		}
		if _, err := bearer.do("GET", "/api/v1/runs/"+url.PathEscape(inv.LastRun.ID)+"/nodes", nil, &nodes); err != nil {
			c.fail("runs nodes %s: %v", inv.LastRun.ID, err)
		} else if int64(len(nodes.Nodes)) != inv.LastRun.Nodes {
			c.fail("runs nodes %s: %d nodes, store held %d", inv.LastRun.ID, len(nodes.Nodes), inv.LastRun.Nodes)
		} else {
			c.pass("runs nodes %s: %d nodes, as stored", inv.LastRun.ID, len(nodes.Nodes))
		}
	}

	var toks struct {
		Tokens []map[string]any `json:"tokens"`
	}
	if _, err := bearer.do("GET", "/api/v1/tokens?include_revoked=1", nil, &toks); err != nil {
		c.fail("tokens list: %v", err)
	} else {
		seen := map[string]bool{}
		revoked := 0
		for _, t := range toks.Tokens {
			if p, ok := t["prefix"].(string); ok {
				seen[p] = true
			}
			if t["revoked_at"] != nil {
				revoked++
			}
		}
		missing := 0
		for _, p := range inv.TokenPrefixes {
			if !seen[p] {
				missing++
			}
		}
		if missing == 0 {
			c.pass("tokens list (include_revoked=1): every one of %d stored prefixes listed, %d revoked", len(inv.TokenPrefixes), revoked)
		} else {
			c.fail("tokens list (include_revoked=1): %d of %d stored prefixes missing", missing, len(inv.TokenPrefixes))
		}
	}
	lookedUp := 0
	for _, p := range inv.TokenPrefixes {
		var tok map[string]any
		if _, err := bearer.do("GET", "/api/v1/tokens/"+url.PathEscape(p), nil, &tok); err != nil {
			c.fail("token lookup by prefix %s: %v", p, err)
			continue
		}
		if tok["team"] != nil && tok["team"] != "default" {
			c.fail("token %s resolves to team %v, want default", p, tok["team"])
		}
		lookedUp++
	}
	c.pass("token lookup by prefix: %d of %d resolve", lookedUp, len(inv.TokenPrefixes))

	var secs struct {
		Secrets []map[string]any `json:"secrets"`
	}
	if _, err := bearer.do("GET", "/api/v1/secrets", nil, &secs); err != nil {
		c.fail("secrets list: %v", err)
	} else if int64(len(secs.Secrets)) != inv.Secrets {
		c.fail("secrets list: %d listed, store held %d", len(secs.Secrets), inv.Secrets)
	} else {
		c.pass("secrets list: %d listed, as stored", len(secs.Secrets))
	}
	canaryDigests := map[string]string{}
	if b, err := os.ReadFile(filepath.Join(*creds, "canary-secrets.json")); err == nil {
		if err := json.Unmarshal(b, &canaryDigests); err != nil {
			return err
		}
	}
	checkSecretValues(&c, bearer, inv.SecretRows, canaryDigests, *keyed)

	var login struct {
		SessionID string `json:"session_id"`
	}
	if _, err := anon.do("POST", "/api/v1/auth/login", map[string]string{"username": user, "password": password}, &login); err != nil {
		c.fail("password login for %s (planted before the upgrade): %v", user, err)
	} else {
		session := client{base: *base, auth: "Session " + login.SessionID}
		var sess map[string]any
		if _, err := session.do("GET", "/api/v1/auth/session", nil, &sess); err != nil {
			c.fail("session lookup after password login: %v", err)
		} else {
			c.pass("password login works; session principal %v team %v", sess["principal"], sess["team"])
		}
		if _, err := session.do("GET", "/api/v1/runs?limit=1", nil, nil); err != nil {
			c.fail("runs list under the password session: %v", err)
		} else {
			c.pass("runs list under the password session")
		}
	}
	var wrong struct{}
	if code, err := anon.do("POST", "/api/v1/auth/login", map[string]string{"username": user, "password": password + "x"}, &wrong); err == nil || code != http.StatusUnauthorized {
		c.fail("a wrong password answered %d, want 401", code)
	} else {
		c.pass("a wrong password is refused (401)")
	}
	return c.err()
}

// safety: values are compared by digest and never printed, because the copy
// holds real credentials.
func checkSecretValues(c *checker, cl client, rows []secretRow, canaries map[string]string, keyed bool) {
	opened, matched := 0, 0
	for _, s := range rows {
		path := "/api/v1/secrets/" + url.PathEscape(s.Name)
		if s.Pipeline != "" {
			path += "?pipeline=" + url.QueryEscape(s.Pipeline)
		}
		var got struct {
			Value string `json:"value"`
			Bound bool   `json:"bound"`
		}
		if _, err := cl.do("GET", path, nil, &got); err != nil {
			c.fail("secret %s (pipeline %q) does not open: %v", s.Name, s.Pipeline, err)
			continue
		}
		opened++
		if keyed && !got.Bound {
			c.fail("secret %s (pipeline %q) is not sealed to its team after a keyed start", s.Name, s.Pipeline)
		}
		want := s.Digest
		if want == "" {
			want = canaries[s.Name+"\x1f"+s.Pipeline]
		}
		if want == "" {
			continue
		}
		sum := sha256.Sum256([]byte(got.Value))
		if hex.EncodeToString(sum[:]) != want {
			c.fail("secret %s (pipeline %q) opens to a different value than was stored", s.Name, s.Pipeline)
			continue
		}
		matched++
	}
	if len(rows) == 0 {
		c.note("the store holds no secrets, so none could be opened")
		return
	}
	c.pass("secrets get: %d of %d open; %d match their pre-upgrade value by digest", opened, len(rows), matched)
}
