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

func TestStorePostgresSelectionKeepsServerTestsAndSkipsSQLiteOnlyOnes(t *testing.T) {
	dir := writeStoreTestFixture(t, map[string]string{
		"external_test.go": `package store_test
import "testing"
func TestExternal(t *testing.T) {}
func TestClaimSecurityRegressions(t *testing.T) {}
func TestPostgresClaimSecurityRegressions(t *testing.T) {}
`,
		"internal_test.go": `package store
import ("os"; "testing")
const pgEnv = "SPARKWING_TEST_PG_URL"
func serverURL() string { return os.Getenv(pgEnv) }
func TestThroughHelper(t *testing.T) { _ = serverURL() }
func TestDirect(t *testing.T) { _ = os.Getenv("SPARKWING_TEST_PG_URL") }
func TestDriver(t *testing.T) { _ = "pgx" }
func TestSQLiteOnly(t *testing.T) { _ = os.Getenv("HOME") }
func FuzzSQLiteOnly(f *testing.F) {}
`,
	})
	listed := []string{"FuzzSQLiteOnly", "TestClaimSecurityRegressions", "TestDirect", "TestDriver", "TestExternal", "TestPostgresClaimSecurityRegressions", "TestSQLiteOnly", "TestThroughHelper"}
	sel, err := selectStorePostgresTests(listed, dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"TestDirect", "TestDriver", "TestExternal", "TestPostgresClaimSecurityRegressions", "TestThroughHelper"}; !slices.Equal(sel.run, want) {
		t.Errorf("run = %v, want %v", sel.run, want)
	}
	if want := []string{"FuzzSQLiteOnly", "TestClaimSecurityRegressions", "TestSQLiteOnly"}; !slices.Equal(sel.skipped, want) {
		t.Errorf("skipped = %v, want %v", sel.skipped, want)
	}

	if _, err := selectStorePostgresTests(append(slices.Clone(listed), "TestUndeclared"), dir); err == nil || !strings.Contains(err.Error(), "no declaration") {
		t.Errorf("undeclared listed test: %v, want refusal", err)
	}
	withoutKept := slices.DeleteFunc(slices.Clone(listed), func(name string) bool { return name == "TestPostgresClaimSecurityRegressions" })
	if _, err := selectStorePostgresTests(withoutKept, dir); err == nil || !strings.Contains(err.Error(), "storePostgresDuplicates") {
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

// safety: guards the real inventory, so a source change that would move a
// PostgreSQL test out of the job fails here rather than going unrun.
func TestStorePostgresSelectionCoversTheStoreInventory(t *testing.T) {
	dir := filepath.Join("..", "..", storePostgresPackageDir)
	decls, err := storeTestDeclarations(dir)
	if err != nil {
		t.Fatal(err)
	}
	listed := make([]string, 0, len(decls))
	for name := range decls {
		listed = append(listed, name)
	}
	slices.Sort(listed)
	sel, err := selectStorePostgresTests(listed, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range listed {
		_, duplicate := storePostgresDuplicates[name]
		if decls[name].external && !duplicate && !slices.Contains(sel.run, name) {
			t.Errorf("external store test %q, which opens through storetest, is not selected", name)
		}
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
