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
	found, warning := localsecrets.FindLegacyFiles()
	if warning != "" {
		t.Fatalf("FindLegacyFiles warned: %s", warning)
	}
	return dir, found
}

func importNow(t *testing.T, st *store.Store, files localsecrets.LegacyFiles) []localsecrets.ImportedFile {
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

	got := importNow(t, st, files)
	if len(got) != 2 || got[0].Imported != 2 || got[1].Imported != 1 {
		t.Fatalf("import = %+v, want 2 from secrets.env and 1 from config.env", got)
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
	body, err := os.ReadFile(filepath.Join(dir, "secrets.env"))
	if err != nil || string(body) != secretsBody {
		t.Errorf("secrets.env after import = %q, %v; want it untouched", body, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets.env.migrated")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("import left a .migrated copy: %v", err)
	}
}

func TestImportLegacy_TheStoreWinsAConflict(t *testing.T) {
	_, files := legacyDir(t, map[string]string{"secrets.env": "TOKEN=from-file\nSAME=equal\nNEW=fresh\n"})
	st := openStore(t)
	ring := loadRing(t)
	sealRow(t, ring.For(st), st, "TOKEN", "from-store")
	sealRow(t, ring.For(st), st, "SAME", "equal")

	got := importNow(t, st, files)
	if len(got) != 1 || got[0].Imported != 1 || strings.Join(got[0].Conflicts, ",") != "TOKEN" {
		t.Fatalf("import = %+v, want NEW imported and only TOKEN in conflict", got)
	}
	if value, err := openRow(t, ring.For(st), st, "TOKEN"); err != nil || value != "from-store" {
		t.Errorf("TOKEN = %q, %v; want the store's value", value, err)
	}
	if notice := got[0].Notice(); !strings.Contains(notice, "TOKEN") || !strings.Contains(notice, "can be deleted") {
		t.Errorf("notice %q names neither the conflict nor what to do with the file", notice)
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

func TestImportLegacy_RunsOnceAndKeepsADeletedNameDeleted(t *testing.T) {
	_, files := legacyDir(t, map[string]string{"secrets.env": "TOKEN=abc\n"})
	st := openStore(t)
	importNow(t, st, files)
	if err := st.DeleteSecret("TOKEN", ""); err != nil {
		t.Fatal(err)
	}

	if again := importNow(t, st, files); len(again) != 0 {
		t.Fatalf("second import = %+v, want nothing imported", again)
	}
	if _, err := st.GetSecretRow("TOKEN", ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("TOKEN after a repeated import = %v, want it still deleted", err)
	}
}

func TestImportLegacy_AnInterruptedImportRunsAgainWhole(t *testing.T) {
	_, files := legacyDir(t, map[string]string{"secrets.env": "TOKEN=abc\nOTHER=def\n"})
	st := openStore(t)
	ctx := context.Background()
	cipher := loadRing(t).For(st)
	boom := errors.New("crash before commit")
	row := store.Secret{Name: "TOKEN", Masked: true, Shared: true, Value: "enc:v3:partial"}
	if err := st.ImportSecrets(ctx, []store.Secret{row}, func([]string) (map[string]string, error) {
		return nil, boom
	}, time.Now()); !errors.Is(err, boom) {
		t.Fatalf("interrupted import = %v, want the injected failure", err)
	}
	if _, err := st.GetSecretRow("TOKEN", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a row outlived the failed transaction: %v", err)
	}

	got, err := localsecrets.ImportLegacy(ctx, st, cipher, files, time.Now())
	if err != nil || len(got) != 1 || got[0].Imported != 2 {
		t.Fatalf("import after the interruption = %+v, %v; want both names", got, err)
	}
}

func TestImportLegacy_AChangedFileImportsItsNewNames(t *testing.T) {
	dir, files := legacyDir(t, map[string]string{"secrets.env": "TOKEN=abc\n"})
	st := openStore(t)
	importNow(t, st, files)
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("TOKEN=abc\nLATER=xyz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := importNow(t, st, files)
	if len(got) != 1 || got[0].Imported != 1 || len(got[0].Conflicts) != 0 {
		t.Fatalf("import of the changed file = %+v, want LATER alone", got)
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

func TestTakeLegacyReport_TellsOnce(t *testing.T) {
	_, files := legacyDir(t, map[string]string{"secrets.env": "TOKEN=abc\n"})
	st := openStore(t)
	importNow(t, st, files)
	ctx := context.Background()

	first, err := localsecrets.TakeLegacyReport(ctx, st, files, time.Now())
	if err != nil || len(first) != 1 || first[0].Imported != 1 {
		t.Fatalf("first report = %+v, %v; want the one file", first, err)
	}
	second, err := localsecrets.TakeLegacyReport(ctx, st, files, time.Now())
	if err != nil || len(second) != 0 {
		t.Fatalf("second report = %+v, %v; want nothing", second, err)
	}
}

func TestFindLegacyFiles_SkipsWhenAPathVariableIsSet(t *testing.T) {
	legacyDir(t, map[string]string{"secrets.env": "TOKEN=abc\n"})
	t.Setenv("SPARKWING_SECRETS", filepath.Join(t.TempDir(), "elsewhere.env"))
	files, warning := localsecrets.FindLegacyFiles()
	if !files.Empty() || !strings.Contains(warning, "SPARKWING_SECRETS") {
		t.Fatalf("FindLegacyFiles = %+v, %q; want no files and a warning naming the variable", files, warning)
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
	files, warning := localsecrets.FindLegacyFiles()
	if !files.Empty() || warning == "" {
		t.Fatalf("FindLegacyFiles under a scratch home = %+v, %q; want no files and a warning", files, warning)
	}
}
