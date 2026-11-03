package jobs

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeStoreTestFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func storeSelectionFixture(t *testing.T) string {
	t.Helper()
	return writeStoreTestFixture(t, map[string]string{
		"external_test.go": `package store_test
import "testing"
func TestExternal(t *testing.T) {}
func TestClaimSecurityRegressions(t *testing.T) {}
func TestPostgresClaimSecurityRegressions(t *testing.T) {}
`,
		"sqlite_test.go": `package store
import ("os"; "testing")
func TestSQLiteOnly(t *testing.T) { _ = os.Getenv("HOME") }
func FuzzSQLiteOnly(f *testing.F) {}
`,
		"helpers.go": `package store
import "os"
const postgresDriverForTest = "pgx"
func postgresURLForTest() string { return os.Getenv("SPARKWING_TEST_PG_URL") }
`,
		"new_test.go": `package store
import ("database/sql"; "testing")
func TestNewPostgresPath(t *testing.T) { _, _ = sql.Open(postgresDriverForTest, postgresURLForTest()) }
func TestNewUnclassified(t *testing.T) {}
`,
		"direct_test.go": `package store
import ("os"; "testing")
const pgEnv = "SPARKWING_TEST_PG_URL"
func serverURL() string { return os.Getenv(pgEnv) }
func TestThroughHelper(t *testing.T) { _ = serverURL() }
`,
	})
}

func storeSelectionListing() []string {
	return []string{"FuzzSQLiteOnly", "TestClaimSecurityRegressions", "TestExternal", "TestNewPostgresPath", "TestNewUnclassified", "TestPostgresClaimSecurityRegressions", "TestSQLiteOnly", "TestThroughHelper"}
}

func TestStorePostgresSelectionRunsEveryTestOutsideTheSQLiteOnlyFiles(t *testing.T) {
	dir := storeSelectionFixture(t)
	sel, err := selectStorePostgresTests(storeSelectionListing(), dir, []string{"sqlite_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	// safety: TestNewPostgresPath reaches the server only through a non-test
	// helper the scan cannot see, so it must run because its file is unlisted.
	if want := []string{"TestExternal", "TestNewPostgresPath", "TestNewUnclassified", "TestPostgresClaimSecurityRegressions", "TestThroughHelper"}; !slices.Equal(sel.run, want) {
		t.Errorf("run = %v, want %v", sel.run, want)
	}
	if want := []string{"FuzzSQLiteOnly", "TestClaimSecurityRegressions", "TestSQLiteOnly"}; !slices.Equal(sel.skipped, want) {
		t.Errorf("skipped = %v, want %v", sel.skipped, want)
	}
}

func TestStorePostgresSelectionRefusesABadSQLiteOnlyList(t *testing.T) {
	dir := storeSelectionFixture(t)
	listed := storeSelectionListing()
	for _, tc := range []struct {
		files []string
		want  string
	}{
		{[]string{"gone_test.go"}, "not a test file"},
		{[]string{"external_test.go"}, "no internal top-level test"},
		{[]string{"direct_test.go"}, "now reaches"},
		{[]string{"sqlite_test.go", "sqlite_test.go"}, "listed twice"},
	} {
		if _, err := selectStorePostgresTests(listed, dir, tc.files); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("list %v: %v, want refusal containing %q", tc.files, err, tc.want)
		}
	}
	if _, err := selectStorePostgresTests(append(slices.Clone(listed), "TestUndeclared"), dir, nil); err == nil || !strings.Contains(err.Error(), "no declaration") {
		t.Errorf("undeclared listed test: %v, want refusal", err)
	}
	withoutKept := slices.DeleteFunc(slices.Clone(listed), func(name string) bool { return name == "TestPostgresClaimSecurityRegressions" })
	if _, err := selectStorePostgresTests(withoutKept, dir, nil); err == nil || !strings.Contains(err.Error(), "storePostgresDuplicates") {
		t.Errorf("duplicate without its kept copy: %v, want refusal", err)
	}
}

func TestStorePostgresPartitionPlacesEveryListedNameOnce(t *testing.T) {
	listed := []string{"TestA", "TestB", "TestC"}
	if err := checkStorePostgresPartition(listed, storePostgresSelection{run: []string{"TestA"}, skipped: []string{"TestB", "TestC"}}); err != nil {
		t.Fatal(err)
	}
	for _, sel := range []storePostgresSelection{
		{run: []string{"TestA"}, skipped: []string{"TestB"}},
		{run: []string{"TestA", "TestB"}, skipped: []string{"TestB", "TestC"}},
		{run: []string{"TestA", "TestD"}, skipped: []string{"TestB", "TestC"}},
	} {
		if err := checkStorePostgresPartition(listed, sel); err == nil {
			t.Errorf("partition %+v accepted", sel)
		}
	}
}

// safety: guards the checked-in list against the real source, so a listed file
// that is renamed, emptied or starts reaching PostgreSQL fails here.
func TestStorePostgresSelectionCoversTheStoreInventory(t *testing.T) {
	dir := filepath.Join("..", "..", storePostgresPackageDir)
	decls, _, err := storeTestDeclarations(dir)
	if err != nil {
		t.Fatal(err)
	}
	listed := make([]string, 0, len(decls))
	for name := range decls {
		listed = append(listed, name)
	}
	slices.Sort(listed)
	sel, err := selectStorePostgresTests(listed, dir, storePostgresSQLiteOnlyFiles(storePostgresSQLiteOnlyList))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"TestTrustMatrix", "TestClaimPinnedSelectorsPreserveDecodedPrefixes", "TestDefaultBranchWorkSQLAgreesWithGo", "TestPostgresClaimSecurityRegressions"} {
		if !slices.Contains(sel.run, name) {
			t.Errorf("postgres test %q is not selected", name)
		}
	}
	if !slices.Contains(sel.skipped, "TestClaimSecurityRegressions") {
		t.Error("the dialect-following claim-security copy still runs beside the PostgreSQL one")
	}
	t.Logf("store inventory: %d listed, %d run on postgres, %d left to the full suite", len(listed), len(sel.run), len(sel.skipped))
}
