package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

type installFingerprint struct {
	Schema  int
	Runs    []string
	Nodes   []string
	Secrets []string
	Balance int64
	Grants  []string
	Tokens  []string
	Users   []string
}

func (f installFingerprint) String() string {
	return fmt.Sprintf("schema=%d\nruns=%v\nnodes=%v\nsecrets=%v\nbalance=%d\ngrants=%v\ntokens=%v\nusers=%v",
		f.Schema, f.Runs, f.Nodes, f.Secrets, f.Balance, f.Grants, f.Tokens, f.Users)
}

// safety: fixed rather than generated, because the drill has to open the restored
// rows under the same key the source install sealed them with.
const drillSecretKey = "sparkwing-backup-drill-key-32b!!"

func drillCipher(t *testing.T) *secrets.Cipher {
	t.Helper()
	c, err := secrets.NewCipher([]byte(drillSecretKey))
	if err != nil {
		t.Fatalf("new drill cipher: %v", err)
	}
	return c
}

func populateInstall(t *testing.T, st *store.Store, cipher *secrets.Cipher) {
	t.Helper()
	ctx := context.Background()
	start := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)

	for i, pipeline := range []string{"build", "deploy", "nightly"} {
		runID := fmt.Sprintf("drill-run-%d", i)
		finished := start.Add(time.Duration(i+1) * time.Minute)
		if err := st.CreateRun(ctx, store.Run{
			ID:            runID,
			Pipeline:      pipeline,
			Status:        "success",
			DeclaredRepo:  "fictional-app",
			GitBranch:     "main",
			GitSHA:        fmt.Sprintf("%040d", i),
			TriggerSource: "webhook",
			Args:          map[string]string{"tier": pipeline},
			CreatedAt:     start,
			StartedAt:     start,
			FinishedAt:    &finished,
		}); err != nil {
			t.Fatalf("create run %s: %v", runID, err)
		}
		for n := range 2 {
			nodeID := fmt.Sprintf("node-%d", n)
			if err := st.CreateNode(ctx, store.Node{
				RunID:      runID,
				NodeID:     nodeID,
				Status:     "done",
				Outcome:    "success",
				Deps:       []string{},
				StartedAt:  &start,
				FinishedAt: &finished,
			}); err != nil {
				t.Fatalf("create node %s/%s: %v", runID, nodeID, err)
			}
		}
		if _, err := st.AppendEvent(ctx, runID, "", "run_start", []byte(`{"drill":true}`)); err != nil {
			t.Fatalf("append event for %s: %v", runID, err)
		}
	}

	for name, plain := range map[string]string{
		"DEPLOY_TOKEN": "ghp-drill-deploy",
		"NPM_TOKEN":    "npm-drill-publish",
	} {
		sealed, err := cipher.SealBound(name, "", false, true, plain)
		if err != nil {
			t.Fatalf("seal %s: %v", name, err)
		}
		if err := st.CreateOrReplaceSecret(store.Secret{
			Name:      name,
			Value:     sealed,
			Principal: "operator",
			Masked:    true,
		}, start); err != nil {
			t.Fatalf("write secret %s: %v", name, err)
		}
	}

	if _, err := st.GrantCredits(ctx, store.CreditGrantPaid, 5_000_000, "drill-invoice-1", "operator"); err != nil {
		t.Fatalf("grant credits: %v", err)
	}
	if _, err := st.GrantCredits(ctx, store.CreditGrantPaid, 2_500_000, "drill-invoice-2", "operator"); err != nil {
		t.Fatalf("grant credits: %v", err)
	}

	if _, _, err := st.CreateToken("runner-1", store.TokenKindRunner, []string{"nodes.claim"}, 0, start); err != nil {
		t.Fatalf("create runner token: %v", err)
	}
	if _, _, err := st.CreateToken("operator", store.TokenKindUser, []string{"admin"}, 0, start); err != nil {
		t.Fatalf("create admin token: %v", err)
	}

	if _, err := st.CreateUser("operator", "drill-password-1", []string{"admin"}, start); err != nil {
		t.Fatalf("create user: %v", err)
	}
}

func fingerprintInstall(t *testing.T, st *store.Store, cipher *secrets.Cipher) installFingerprint {
	t.Helper()
	ctx := context.Background()
	var f installFingerprint

	schema, err := st.CurrentSchemaVersion(ctx)
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	f.Schema = schema

	runs, err := st.ListRuns(ctx, store.RunFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	for _, r := range runs {
		f.Runs = append(f.Runs, fmt.Sprintf("%s/%s/%s/%s", r.ID, r.Pipeline, r.Status, r.GitSHA))
		nodes, err := st.ListNodes(ctx, r.ID)
		if err != nil {
			t.Fatalf("list nodes for %s: %v", r.ID, err)
		}
		for _, n := range nodes {
			f.Nodes = append(f.Nodes, fmt.Sprintf("%s/%s/%s/%s", r.ID, n.NodeID, n.Status, n.Outcome))
		}
	}

	list, err := st.ListSecrets()
	if err != nil {
		t.Fatalf("list secrets: %v", err)
	}
	for _, sec := range list {
		row, err := st.GetSecretRow(sec.Name, sec.Pipeline)
		if err != nil {
			t.Fatalf("read secret %s: %v", sec.Name, err)
		}
		plain, err := cipher.OpenBound(row.Name, row.Pipeline, row.Shared, row.Masked, row.Value)
		if err != nil {
			t.Fatalf("open secret %s: %v", sec.Name, err)
		}
		f.Secrets = append(f.Secrets, sec.Name+"="+plain)
	}

	balance, err := st.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("read credit balance: %v", err)
	}
	f.Balance = balance
	grants, err := st.ListCreditGrants(ctx, 100)
	if err != nil {
		t.Fatalf("list credit grants: %v", err)
	}
	for _, g := range grants {
		f.Grants = append(f.Grants, fmt.Sprintf("%s/%s/%d", g.Kind, g.Reference, g.AmountMicro))
	}

	tokens, err := st.ListTokens("", true)
	if err != nil {
		t.Fatalf("list tokens: %v", err)
	}
	for _, tok := range tokens {
		f.Tokens = append(f.Tokens, fmt.Sprintf("%s/%s/%s", tok.Prefix, tok.Principal, tok.Kind))
	}

	users, err := st.ListUsers()
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	for _, u := range users {
		f.Users = append(f.Users, u.Name)
	}

	sort.Strings(f.Runs)
	sort.Strings(f.Nodes)
	sort.Strings(f.Secrets)
	sort.Strings(f.Grants)
	sort.Strings(f.Tokens)
	sort.Strings(f.Users)
	return f
}

func assertSameInstall(t *testing.T, want, got installFingerprint) {
	t.Helper()
	if want.String() != got.String() {
		t.Fatalf("the restored store does not serve the source install\nsource:\n%s\n\nrestored:\n%s",
			want.String(), got.String())
	}
	if len(got.Runs) == 0 || len(got.Nodes) == 0 || len(got.Secrets) == 0 || len(got.Tokens) == 0 {
		t.Fatalf("the fingerprint is too empty to prove anything:\n%s", got.String())
	}
	if got.Balance == 0 {
		t.Fatalf("the restored credit balance is zero:\n%s", got.String())
	}
}

// TestBackupRestoreDrill runs the procedure in docs/backup-restore.md end
// to end, so the page cannot drift away from what works. Each subtest
// populates one of every record class a controller holds, backs the store
// up the way the page says to, restores it somewhere else, and compares
// what the two stores serve: runs, nodes, secret values opened under the
// backed-up key, the credit ledger, API tokens and dashboard users.
//
// The "sqlite without the secrets key" subtest is why the key is part of
// the backup rather than a detail of the deployment: the rows restore, and
// every value in them stays shut.
//
// The name carries no dialect because the Postgres subtest is gated on
// client binaries as well as on a server, which is a narrower gate than
// the hosted Postgres lane promises to satisfy.
func TestBackupRestoreDrill(t *testing.T) {
	t.Run("sqlite", drillSQLite)
	t.Run("sqlite without the secrets key", drillSQLiteWithoutKey)
	t.Run("postgres", drillPostgres)
}

func drillSQLite(t *testing.T) {
	cipher := drillCipher(t)
	live := filepath.Join(t.TempDir(), "state.db")

	st, err := store.Open(live)
	if err != nil {
		t.Fatalf("open live store: %v", err)
	}
	populateInstall(t, st, cipher)
	source := fingerprintInstall(t, st, cipher)
	// safety: a SQLite backup is only consistent once the writer has released the
	// database and checkpointed its write-ahead log.
	if err := st.Close(); err != nil {
		t.Fatalf("close live store: %v", err)
	}

	backup := filepath.Join(t.TempDir(), "backup")
	if err := os.MkdirAll(backup, 0o700); err != nil {
		t.Fatalf("make backup dir: %v", err)
	}
	copied := 0
	for _, suffix := range []string{"", "-wal", "-shm"} {
		body, err := os.ReadFile(live + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", live+suffix, err)
		}
		if err := os.WriteFile(filepath.Join(backup, "state.db"+suffix), body, 0o600); err != nil {
			t.Fatalf("write backup %s: %v", suffix, err)
		}
		copied++
	}
	if copied == 0 {
		t.Fatal("the backup copied no files")
	}

	restored := filepath.Join(t.TempDir(), "restored")
	if err := os.MkdirAll(restored, 0o700); err != nil {
		t.Fatalf("make restore dir: %v", err)
	}
	entries, err := os.ReadDir(backup)
	if err != nil {
		t.Fatalf("read backup dir: %v", err)
	}
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join(backup, e.Name()))
		if err != nil {
			t.Fatalf("read backup %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(restored, e.Name()), body, 0o600); err != nil {
			t.Fatalf("write restored %s: %v", e.Name(), err)
		}
	}

	rst, err := store.Open(filepath.Join(restored, "state.db"))
	if err != nil {
		t.Fatalf("open restored store: %v", err)
	}
	defer func() {
		if err := rst.Close(); err != nil {
			t.Errorf("close restored store: %v", err)
		}
	}()
	assertSameInstall(t, source, fingerprintInstall(t, rst, cipher))
}

func drillSQLiteWithoutKey(t *testing.T) {
	cipher := drillCipher(t)
	live := filepath.Join(t.TempDir(), "state.db")
	st, err := store.Open(live)
	if err != nil {
		t.Fatalf("open live store: %v", err)
	}
	populateInstall(t, st, cipher)
	if err := st.Close(); err != nil {
		t.Fatalf("close live store: %v", err)
	}

	restored := filepath.Join(t.TempDir(), "state.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		body, err := os.ReadFile(live + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", live+suffix, err)
		}
		if err := os.WriteFile(restored+suffix, body, 0o600); err != nil {
			t.Fatalf("write %s: %v", restored+suffix, err)
		}
	}

	rst, err := store.Open(restored)
	if err != nil {
		t.Fatalf("open restored store: %v", err)
	}
	defer func() { _ = rst.Close() }()

	other, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("generate other key: %v", err)
	}
	wrong, err := secrets.NewCipher(other)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	list, err := rst.ListSecrets()
	if err != nil {
		t.Fatalf("list secrets: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("the restored store holds no secrets, so the check proves nothing")
	}
	for _, sec := range list {
		row, err := rst.GetSecretRow(sec.Name, sec.Pipeline)
		if err != nil {
			t.Fatalf("read secret %s: %v", sec.Name, err)
		}
		if !secrets.IsEncrypted(row.Value) {
			t.Fatalf("secret %s restored unsealed", sec.Name)
		}
		if _, err := wrong.OpenBound(row.Name, row.Pipeline, row.Shared, row.Masked, row.Value); err == nil {
			t.Fatalf("secret %s opened under a key the install never used", sec.Name)
		}
	}
}

func drillPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: dumps and restores a populated database; the fast class runs under -short")
	}
	base := storetest.PostgresURL(t)
	dump := drillPGTool(t, "pg_dump")
	restore := drillPGTool(t, "pg_restore")
	drillRequireClientNotOlderThanServer(t, base, dump)

	cipher := drillCipher(t)
	unique := storetest.Unique()
	sourceDB := "sw_drill_src_" + unique
	restoredDB := "sw_drill_dst_" + unique
	drillCreateDatabase(t, base, sourceDB)
	drillCreateDatabase(t, base, restoredDB)

	sourceDSN := drillDatabaseDSN(t, base, sourceDB)
	restoredDSN := drillDatabaseDSN(t, base, restoredDB)

	st, err := store.OpenPostgres(context.Background(), sourceDSN)
	if err != nil {
		t.Fatalf("open source store: %v", err)
	}
	populateInstall(t, st, cipher)
	source := fingerprintInstall(t, st, cipher)
	if err := st.Close(); err != nil {
		t.Fatalf("close source store: %v", err)
	}

	archive := filepath.Join(t.TempDir(), "controller.dump")
	drillRun(t, dump, "--format=custom", "--no-owner", "--no-privileges",
		"--file="+archive, sourceDSN)
	info, err := os.Stat(archive)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("pg_dump wrote an empty archive")
	}

	drillRun(t, restore, "--no-owner", "--no-privileges", "--exit-on-error",
		"--dbname="+restoredDSN, archive)

	rst, err := store.OpenPostgres(context.Background(), restoredDSN)
	if err != nil {
		t.Fatalf("open restored store: %v", err)
	}
	defer func() {
		if err := rst.Close(); err != nil {
			t.Errorf("close restored store: %v", err)
		}
	}()
	assertSameInstall(t, source, fingerprintInstall(t, rst, cipher))
}

// safety: SPARKWING_PG_BIN names client binaries at least as new as the server,
// because a pg_dump older than the server it reads refuses the dump outright.
// drillRequireClientNotOlderThanServer skips when pg_dump is older than the
// server, because pg_dump refuses a newer server outright and the refusal is
// an environment fact rather than a defect in the code under test. A lane that
// must not skip turns this into a failure with its own no-skip guard.
func drillRequireClientNotOlderThanServer(t *testing.T, baseURL, dump string) {
	t.Helper()
	db, err := sql.Open("pgx", baseURL)
	if err != nil {
		t.Fatalf("open for server version: %v", err)
	}
	defer func() { _ = db.Close() }()
	var server string
	if err := db.QueryRow("SHOW server_version").Scan(&server); err != nil {
		t.Fatalf("read server version: %v", err)
	}
	out, err := exec.Command(dump, "--version").CombinedOutput()
	if err != nil {
		t.Skipf("%s --version failed, so the client version is unknown: %v", dump, err)
	}
	client := string(out)
	sMajor, cMajor := drillMajor(server), drillMajor(client)
	if sMajor == 0 || cMajor == 0 {
		t.Skipf("could not read a major version from server %q or client %q", server, strings.TrimSpace(client))
	}
	if cMajor < sMajor {
		t.Skipf("pg_dump %d is older than server %d, so the dump would be refused. "+
			"Point SPARKWING_PG_BIN at a client at least as new as the server.", cMajor, sMajor)
	}
}

func drillMajor(v string) int {
	for _, f := range strings.Fields(v) {
		digits := strings.TrimLeft(f, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ()")
		cut := strings.IndexFunc(digits, func(r rune) bool { return r < '0' || r > '9' })
		if cut > 0 {
			digits = digits[:cut]
		}
		if n, err := strconv.Atoi(digits); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

func drillPGTool(t *testing.T, name string) string {
	t.Helper()
	if dir := strings.TrimSpace(os.Getenv("SPARKWING_PG_BIN")); dir != "" {
		return filepath.Join(dir, name)
	}
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not on PATH and SPARKWING_PG_BIN is unset, so the Postgres backup drill "+
			"cannot run. Install a PostgreSQL client at least as new as the server, or point "+
			"SPARKWING_PG_BIN at its bin directory.", name)
	}
	return path
}

func drillRun(t *testing.T, program string, args ...string) {
	t.Helper()
	cmd := exec.Command(program, args...)
	cmd.Env = append(os.Environ(), "PGCONNECT_TIMEOUT=10")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", filepath.Base(program), strings.Join(args, " "), err, out)
	}
}

func drillCreateDatabase(t *testing.T, baseDSN, name string) {
	t.Helper()
	admin, err := store.OpenPostgres(context.Background(), baseDSN)
	if err != nil {
		t.Fatalf("open admin postgres: %v", err)
	}
	if _, err := admin.DB().Exec(`CREATE DATABASE ` + name); err != nil {
		_ = admin.Close()
		t.Fatalf("create database %s: %v", name, err)
	}
	if err := admin.Close(); err != nil {
		t.Fatalf("close admin postgres: %v", err)
	}
	t.Cleanup(func() {
		// safety: t.Context is cancelled before Cleanup runs, so the drop
		// opens its own connection.
		cleanup, err := store.OpenPostgres(context.Background(), baseDSN)
		if err != nil {
			t.Errorf("open postgres to drop database %s: %v", name, err)
			return
		}
		if _, err := cleanup.DB().Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
		if err := cleanup.Close(); err != nil {
			t.Errorf("close postgres after dropping database %s: %v", name, err)
		}
	})
}

func drillDatabaseDSN(t *testing.T, baseDSN, name string) string {
	t.Helper()
	u, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatalf("parse %s: %v", storetest.URLEnv, err)
	}
	u.Path = "/" + name
	q := u.Query()
	q.Del("search_path")
	u.RawQuery = q.Encode()
	return u.String()
}
