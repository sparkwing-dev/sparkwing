package localsecrets_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/localsecrets"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the sandbox guard lets a test read dotenv files only from the test
// binary's own sandbox.
func legacyDir(t *testing.T, files map[string]string) (string, localsecrets.LegacyFiles) {
	t.Helper()
	if err := os.MkdirAll(paths.TestSandbox(), 0o700); err != nil {
		t.Fatal(err)
	}
	xdg, err := os.MkdirTemp(paths.TestSandbox(), "xdg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(xdg) })
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("SPARKWING_HOME", "")
	t.Setenv("SPARKWING_SECRETS", "")
	t.Setenv("SPARKWING_CONFIG_ENV", "")
	dir := filepath.Join(xdg, "sparkwing")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	keyFileIn(t, dir)
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	found, err := localsecrets.FindLegacyFiles()
	if err != nil {
		t.Fatalf("FindLegacyFiles: %v", err)
	}
	return dir, found
}

func importNow(t *testing.T, st *store.Store, files localsecrets.LegacyFiles) *localsecrets.ImportResult {
	t.Helper()
	got, err := localsecrets.ImportLegacy(context.Background(), st, loadRing(t).For(st), files, time.Now())
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	return got
}

func TestImportLegacy_SealsBothFilesAsSharedRowsAndLeavesThemInPlace(t *testing.T) {
	secretsBody := "TOKEN=abc123\nexport QUOTED=\"two words\"\n"
	dir, files := legacyDir(t, map[string]string{
		"secrets.env": secretsBody,
		"config.env":  "REGION=us-east-1\n",
	})
	st := openStore(t)

	if got := importNow(t, st, files); strings.Join(got.Imported, ",") != "QUOTED,REGION,TOKEN" {
		t.Fatalf("import = %+v, want all three names", got)
	}
	ring := loadRing(t)
	for name, want := range map[string]struct {
		value  string
		masked bool
	}{"TOKEN": {"abc123", true}, "QUOTED": {"two words", true}, "REGION": {"us-east-1", false}} {
		sec, err := st.GetSecretRow(name, "")
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !sec.Shared || sec.Pipeline != "" || sec.Masked != want.masked || !secrets.IsBound(sec.Value) {
			t.Errorf("%s = shared %v pipeline %q masked %v bound %v; want shared, unscoped, masked %v, sealed",
				name, sec.Shared, sec.Pipeline, sec.Masked, secrets.IsBound(sec.Value), want.masked)
		}
		if value, err := openRow(t, ring.For(st), st, name); err != nil || value != want.value {
			t.Errorf("%s opened as %q, %v; want %q", name, value, err, want.value)
		}
	}
	if body, err := os.ReadFile(filepath.Join(dir, "secrets.env")); err != nil || string(body) != secretsBody {
		t.Errorf("secrets.env after import = %q, %v; want it untouched", body, err)
	}
}

func TestImportLegacy_TheStoreWinsAConflict(t *testing.T) {
	_, files := legacyDir(t, map[string]string{"secrets.env": "TOKEN=from-file\nSAME=equal\nNEW=fresh\n"})
	st := openStore(t)
	ring := loadRing(t)
	sealRow(t, ring.For(st), st, "TOKEN", "from-store")
	sealRow(t, ring.For(st), st, "SAME", "equal")

	got := importNow(t, st, files)
	if strings.Join(got.Imported, ",") != "NEW" || strings.Join(got.Conflicts, ",") != "TOKEN" {
		t.Fatalf("import = %+v, want NEW imported and only TOKEN in conflict", got)
	}
	if value, err := openRow(t, ring.For(st), st, "TOKEN"); err != nil || value != "from-store" {
		t.Errorf("TOKEN = %q, %v; want the store's value", value, err)
	}
}

func TestImportLegacy_ANameInBothFilesImportsMasked(t *testing.T) {
	_, files := legacyDir(t, map[string]string{
		"secrets.env": "MODE=masked-copy\n",
		"config.env":  "MODE=plain-copy\n",
	})
	st := openStore(t)
	importNow(t, st, files)

	sec, err := st.GetSecretRow("MODE", "")
	if err != nil {
		t.Fatal(err)
	}
	if !sec.Masked {
		t.Error("MODE imported unmasked, want masked")
	}
	if value, err := openRow(t, loadRing(t).For(st), st, "MODE"); err != nil || value != "plain-copy" {
		t.Errorf("MODE = %q, %v; want the value runs read, plain-copy", value, err)
	}
}

func TestImportLegacy_RunsOnceSoADeletedNameStaysDeleted(t *testing.T) {
	dir, files := legacyDir(t, map[string]string{"secrets.env": "TOKEN=abc\n"})
	st := openStore(t)
	importNow(t, st, files)
	if err := st.DeleteSecret("TOKEN", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("TOKEN=abc\nLATER=xyz\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if again := importNow(t, st, files); again != nil {
		t.Fatalf("second import = %+v, want none", again)
	}
	for _, name := range []string{"TOKEN", "LATER"} {
		if _, err := st.GetSecretRow(name, ""); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s after a second import = %v, want absent", name, err)
		}
	}
	if fresh := importNow(t, openStore(t), files); strings.Join(fresh.Imported, ",") != "LATER,TOKEN" {
		t.Fatalf("import into a fresh store = %+v, want both names", fresh)
	}
}

func TestImportLegacy_AMalformedFileImportsNothingUntilFixed(t *testing.T) {
	dir, files := legacyDir(t, map[string]string{
		"secrets.env": "TOKEN=abc\n",
		"config.env":  "REGION=us-east-1\nnot a dotenv line\n",
	})
	st := openStore(t)
	ctx := context.Background()

	_, err := localsecrets.ImportLegacy(ctx, st, loadRing(t).For(st), files, time.Now())
	if err == nil || !strings.Contains(err.Error(), "config.env:2") {
		t.Fatalf("import of a malformed file = %v, want an error naming config.env:2", err)
	}
	for _, name := range []string{"TOKEN", "REGION"} {
		if _, gerr := st.GetSecretRow(name, ""); !errors.Is(gerr, store.ErrNotFound) {
			t.Errorf("%s was imported beside a malformed file: %v", name, gerr)
		}
	}
	if perr := localsecrets.PendingImport(ctx, st); perr == nil {
		t.Error("PendingImport after a failed import = nil, want the files named")
	}

	if err := os.WriteFile(filepath.Join(dir, "config.env"), []byte("REGION=us-east-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := importNow(t, st, files); strings.Join(got.Imported, ",") != "REGION,TOKEN" {
		t.Fatalf("import after the fix = %+v, want both names", got)
	}
	if perr := localsecrets.PendingImport(ctx, st); perr != nil {
		t.Errorf("PendingImport after the import = %v, want nil", perr)
	}
}

func TestImportSecrets_ARowAlreadyPresentFailsTheWholeImport(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	if err := st.CreateOrReplaceSecret(store.Secret{Name: "B", Value: "x"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	rows := []store.Secret{{Name: "A", Value: "enc:a"}, {Name: "B", Value: "enc:b"}}
	if err := st.ImportSecrets(ctx, rows, "mark", "{}", time.Now()); err == nil {
		t.Fatal("ImportSecrets over an existing row succeeded")
	}
	if _, err := st.GetSecretRow("A", ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("A outlived the failed transaction: %v", err)
	}
	if _, done, err := st.ImportMark(ctx, "mark"); err != nil || done {
		t.Errorf("mark after the failed transaction = %v, %v; want unset", done, err)
	}
}

func TestImportLegacy_RefusesWithoutTheKeyTheStoreNeeds(t *testing.T) {
	_, files := legacyDir(t, map[string]string{"secrets.env": "TOKEN=abc\n"})
	st := openStore(t)
	sealRow(t, loadRing(t).For(st), st, "EXISTING", "x")
	keyFileIn(t, t.TempDir())

	_, err := localsecrets.ImportLegacy(context.Background(), st, loadRing(t).For(st), files, time.Now())
	if !errors.Is(err, localsecrets.ErrNoKey) {
		t.Fatalf("import without the key = %v, want ErrNoKey", err)
	}
	if _, gerr := st.GetSecretRow("TOKEN", ""); !errors.Is(gerr, store.ErrNotFound) {
		t.Errorf("TOKEN was imported without the key: %v", gerr)
	}
}

func TestFindLegacyFiles_ReadsTheFileAPathVariableNames(t *testing.T) {
	legacyDir(t, map[string]string{"secrets.env": "TOKEN=abc\n"})
	elsewhere, err := os.MkdirTemp(paths.TestSandbox(), "elsewhere-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(elsewhere) })
	named := filepath.Join(elsewhere, "mine.env")
	if err := os.WriteFile(named, []byte("MINE=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_SECRETS", named)
	files, err := localsecrets.FindLegacyFiles()
	if err != nil || files.Secrets != named {
		t.Fatalf("FindLegacyFiles = %+v, %v; want the file SPARKWING_SECRETS names", files, err)
	}
}

func TestFindLegacyFiles_SkipsTheMachinesFilesUnderAHomeOfItsOwn(t *testing.T) {
	legacyDir(t, map[string]string{"secrets.env": "TOKEN=abc\n"})
	machine := t.TempDir()
	if err := os.MkdirAll(filepath.Join(machine, "sparkwing"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machine, "sparkwing", "secrets.env"), []byte("TOKEN=abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", machine)
	t.Setenv("SPARKWING_HOME", t.TempDir())
	if files, err := localsecrets.FindLegacyFiles(); err != nil || !files.Empty() {
		t.Fatalf("FindLegacyFiles under a scratch home = %+v, %v; want none", files, err)
	}
}

func TestFindLegacyFiles_AnUnreadableDirectoryFailsTheImport(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads through permissions")
	}
	dir, _ := legacyDir(t, map[string]string{"config.env": "REGION=us-east-1\n"})
	if files, err := localsecrets.FindLegacyFiles(); err != nil || files.Config == "" {
		t.Fatalf("FindLegacyFiles = %+v, %v; want config.env", files, err)
	}
	locked := filepath.Join(dir, "locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "mine.env"), []byte("MINE=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	makeLegacyDirectoryUnreadable(t, locked)
	t.Setenv("SPARKWING_SECRETS", filepath.Join(locked, "mine.env"))
	if files, err := localsecrets.FindLegacyFiles(); err == nil {
		t.Fatalf("FindLegacyFiles past an unreadable directory = %+v, want an error", files)
	}
}

func TestImportLegacy_AFileGoneBeforeTheReadImportsNothing(t *testing.T) {
	dir, files := legacyDir(t, map[string]string{"secrets.env": "TOKEN=abc\n", "config.env": "REGION=x\n"})
	st := openStore(t)
	if err := os.Remove(filepath.Join(dir, "secrets.env")); err != nil {
		t.Fatal(err)
	}
	if _, err := localsecrets.ImportLegacy(context.Background(), st, loadRing(t).For(st), files, time.Now()); err == nil {
		t.Fatal("import with a discovered file gone succeeded")
	}
	if _, err := st.GetSecretRow("REGION", ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("REGION was imported without secrets.env: %v", err)
	}
}
