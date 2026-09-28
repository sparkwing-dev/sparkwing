package localsecrets

// hack: this file imports the dotenv files local secrets lived in before the
// runs store held them. A later release deletes it with its callers and
// pkg/store/secrets_legacy_import.go.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/configguard"
	"github.com/sparkwing-dev/sparkwing/internal/dotenv"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const importMark = "legacy_dotenv_import"

// ImportPrincipal is recorded as the writer of every imported row.
const ImportPrincipal = "local-dotenv-import"

// LegacyFiles names the dotenv files an import reads. Secrets is the masked
// file and Config the --plain one; an empty field is a file that does not
// exist.
type LegacyFiles struct {
	Secrets string `json:"secrets,omitempty"`
	Config  string `json:"config,omitempty"`
}

// Empty reports whether neither file exists.
func (f LegacyFiles) Empty() bool { return f.Secrets == "" && f.Config == "" }

// ImportResult is what the one import did: the names it added, and the names
// the store already held with a different value, which kept the store's
// value. It is recorded in the store with the rows.
type ImportResult struct {
	Files     LegacyFiles `json:"files"`
	Imported  []string    `json:"imported"`
	Conflicts []string    `json:"conflicts,omitempty"`
}

// FindLegacyFiles reports the dotenv files an import reads: the files
// SPARKWING_SECRETS and SPARKWING_CONFIG_ENV name, as older releases read
// them, else secrets.env and config.env in the config directory. A command
// under a sparkwing home of its own finds none, because the files belong to
// the machine.
func FindLegacyFiles() (LegacyFiles, error) {
	dir, err := userconfig.LegacyDir()
	if err != nil {
		return LegacyFiles{}, err
	}
	var files LegacyFiles
	for _, f := range []struct {
		set, name string
		into      *string
	}{
		{os.Getenv("SPARKWING_SECRETS"), "secrets.env", &files.Secrets},
		{os.Getenv("SPARKWING_CONFIG_ENV"), "config.env", &files.Config},
	} {
		p := f.set
		if p == "" {
			p = filepath.Join(dir, f.name)
		}
		if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return LegacyFiles{}, fmt.Errorf("import local secrets: %w", err)
		}
		if outsideSandbox(p) {
			return LegacyFiles{}, nil
		}
		*f.into = p
	}
	return files, nil
}

// ImportLegacy copies the entries of files into st as unscoped shared rows of
// the default team, once per store: it returns nil once any import has been
// recorded, whatever the files hold now, so a name deleted afterwards stays
// deleted. A name the store already holds keeps the store's value. A name in
// both files imports once, masked. Both files are read and parsed before
// anything is written, and the rows and the record commit in one
// transaction, so a failure imports nothing and the next call tries again.
// The files themselves are never changed.
func ImportLegacy(ctx context.Context, st *store.Store, c *Cipher, files LegacyFiles, now time.Time) (*ImportResult, error) {
	if files.Empty() {
		return nil, nil
	}
	if _, done, err := st.ImportMark(ctx, importMark); err != nil || done {
		return nil, err
	}
	entries := map[string]legacyEntry{}
	for _, f := range []struct {
		path   string
		masked bool
	}{{files.Config, false}, {files.Secrets, true}} {
		if f.path == "" {
			continue
		}
		values, err := parseDotenv(f.path)
		if err != nil {
			return nil, fmt.Errorf("import local secrets: %w", err)
		}
		// safety: a run read the --plain file's value first, so a name in both
		// keeps that value and is masked.
		for name, value := range values {
			if prior, both := entries[name]; both {
				value = prior.value
			}
			entries[name] = legacyEntry{value: value, masked: f.masked}
		}
	}
	if err := c.EnsureKey(ctx); err != nil {
		return nil, fmt.Errorf("import local secrets: %w", err)
	}

	result := &ImportResult{Files: files, Imported: []string{}}
	var rows []store.Secret
	for name, e := range entries {
		existing, err := st.GetSecretRow(name, "")
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("import local secrets: read %s: %w", name, err)
		}
		if existing != nil {
			if held, oerr := controller.OpenSecretValue(c, store.DefaultTeam, existing); oerr != nil || held != e.value {
				result.Conflicts = append(result.Conflicts, name)
			}
			continue
		}
		row := store.Secret{Name: name, Principal: ImportPrincipal, Masked: e.masked, Shared: true}
		if row.Value, err = controller.SealSecretValue(c, store.DefaultTeam, &row, e.value); err != nil {
			return nil, fmt.Errorf("import local secrets: seal %s: %w", name, err)
		}
		rows = append(rows, row)
		result.Imported = append(result.Imported, name)
	}
	sort.Strings(result.Imported)
	sort.Strings(result.Conflicts)
	record, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if err := st.ImportSecrets(ctx, rows, importMark, string(record), now); err != nil {
		return nil, fmt.Errorf("import local secrets: %w", err)
	}
	return result, nil
}

// PendingImport reports an error when a dotenv file exists that no import
// has read into st yet, or that exists beside no store at all (nil st), so a run reading st directly fails rather than run
// without those secrets.
func PendingImport(ctx context.Context, st *store.Store) error {
	files, err := FindLegacyFiles()
	if err != nil || files.Empty() {
		return err
	}
	if st != nil {
		if _, done, err := st.ImportMark(ctx, importMark); err != nil || done {
			return err
		}
	}
	return fmt.Errorf("%s have not been imported into the local secret store yet; the sparkwing daemon imports them "+
		"when it opens the store, so run `sparkwing secrets list` and fix any error it reports", describe(files))
}

func describe(f LegacyFiles) string {
	switch {
	case f.Secrets == "":
		return f.Config
	case f.Config == "":
		return f.Secrets
	}
	return f.Secrets + " and " + f.Config
}

func outsideSandbox(path string) bool {
	return configguard.GuardWrite("the local secrets file "+filepath.Base(path), "", path) != nil
}

type legacyEntry struct {
	value  string
	masked bool
}

func parseDotenv(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		key, value, err := dotenv.ParseLine(sc.Text())
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		if key != "" {
			out[key] = value
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}
