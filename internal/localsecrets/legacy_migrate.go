package localsecrets

// hack: this file imports the dotenv files local secrets lived in before the
// runs store held them. A later release deletes it with its callers and
// pkg/store/secrets_legacy_import.go.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/configguard"
	"github.com/sparkwing-dev/sparkwing/internal/dotenv"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	legacySecretsName = "secrets.env"
	legacyConfigName  = "config.env"

	legacyMarkPrefix = "legacy_dotenv_import:"

	// ImportPrincipal is recorded as the writer of every imported row.
	ImportPrincipal = "local-dotenv-import"
)

// LegacyFiles names the dotenv files an import reads. Secrets is the masked
// file and Config the --plain one; an empty field is a file that does not
// exist.
type LegacyFiles struct {
	Secrets string `json:"secrets,omitempty"`
	Config  string `json:"config,omitempty"`
}

// Empty reports whether neither file exists.
func (f LegacyFiles) Empty() bool { return f.Secrets == "" && f.Config == "" }

// ImportedFile is what an import did with one dotenv file: how many names it
// added, and the names the store already held with a different value, which
// kept the store's value.
type ImportedFile struct {
	Path      string   `json:"path"`
	Imported  int      `json:"imported"`
	Conflicts []string `json:"conflicts,omitempty"`
}

// Notice is the one-time message an import prints for f.
func (f ImportedFile) Notice() string {
	var b strings.Builder
	fmt.Fprintf(&b, "sparkwing: imported %d secrets from %s into the local secret store; %s is no longer read and can be deleted",
		f.Imported, f.Path, f.Path)
	if len(f.Conflicts) > 0 {
		fmt.Fprintf(&b, "\nsparkwing: %s also sets %s, which the store already held with a different value; the store's value stays",
			f.Path, strings.Join(f.Conflicts, ", "))
	}
	return b.String()
}

// FindLegacyFiles reports the dotenv files present in the config directory.
// It returns a warning in place of files when an import must not read them:
// a variable that once moved them is set, so the files it named are not the
// ones found here, or this command runs under a sparkwing home of its own
// and the files belong to the machine.
func FindLegacyFiles() (LegacyFiles, string) {
	var set []string
	if os.Getenv("SPARKWING_SECRETS") != "" {
		set = append(set, "SPARKWING_SECRETS")
	}
	if os.Getenv("SPARKWING_CONFIG_ENV") != "" {
		set = append(set, "SPARKWING_CONFIG_ENV")
	}
	dir, err := userconfig.LegacyDir()
	if err != nil {
		return LegacyFiles{}, ""
	}
	var files LegacyFiles
	for _, f := range []struct {
		name string
		into *string
	}{{legacySecretsName, &files.Secrets}, {legacyConfigName, &files.Config}} {
		p := filepath.Join(dir, f.name)
		if _, err := os.Lstat(p); err == nil {
			*f.into = p
		}
	}
	if len(set) > 0 {
		return LegacyFiles{}, fmt.Sprintf("sparkwing: %s no longer names a secrets file, and the local secret store does not import one; "+
			"add each value with `sparkwing secrets set` and unset %s", strings.Join(set, " and "), strings.Join(set, " and "))
	}
	for _, p := range []string{files.Secrets, files.Config} {
		if p == "" {
			continue
		}
		if err := configguard.GuardWrite("the local secrets file "+filepath.Base(p), "", p); err != nil {
			return LegacyFiles{}, fmt.Sprintf("sparkwing: not importing %s into this home's secret store: %v", p, err)
		}
	}
	return files, ""
}

type legacyMark struct {
	SHA256    string   `json:"sha256"`
	Imported  int      `json:"imported"`
	Conflicts []string `json:"conflicts,omitempty"`
	Reported  bool     `json:"reported"`
}

type legacyEntry struct {
	value  string
	masked bool
	from   string
}

// ImportLegacy copies the entries of files into st as unscoped shared rows of
// the default team and returns what it did for each file this call imported.
// A name the store already holds keeps the store's value. A name in both
// files imports once, masked. A file whose content was imported before is
// skipped, so a name deleted from the store after its import stays deleted;
// the files themselves are never changed.
//
// safety: the key is ensured before anything is sealed, and every row and the
// record of each file's import commit in one transaction, so a crash leaves
// either nothing imported or all of it recorded.
func ImportLegacy(ctx context.Context, st *store.Store, c *Cipher, files LegacyFiles, now time.Time) ([]ImportedFile, error) {
	type pending struct {
		path   string
		masked bool
		sum    string
		values map[string]string
	}
	var todo []pending
	for _, f := range []struct {
		path   string
		masked bool
	}{{files.Secrets, true}, {files.Config, false}} {
		if f.path == "" {
			continue
		}
		data, err := os.ReadFile(f.path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.path, err)
		}
		digest := sha256.Sum256(data)
		sum := hex.EncodeToString(digest[:])
		mark, found, err := readMark(ctx, st, f.path)
		if err != nil {
			return nil, err
		}
		if found && mark.SHA256 == sum {
			continue
		}
		values, err := parseDotenv(f.path, data)
		if err != nil {
			return nil, err
		}
		todo = append(todo, pending{path: f.path, masked: f.masked, sum: sum, values: values})
	}
	if len(todo) == 0 {
		return nil, nil
	}
	if err := c.EnsureKey(ctx); err != nil {
		return nil, fmt.Errorf("import local secrets: %w", err)
	}

	entries := map[string]legacyEntry{}
	for _, p := range todo {
		for name, value := range p.values {
			prior, both := entries[name]
			switch {
			case !both:
				entries[name] = legacyEntry{value: value, masked: p.masked, from: p.path}
			case p.masked:
				// safety: a run read the --plain file's value first, so that value
				// is the one it used; masking it is the safe way to keep it.
				entries[name] = legacyEntry{value: prior.value, masked: true, from: p.path}
			default:
				entries[name] = legacyEntry{value: value, masked: true, from: prior.from}
			}
		}
	}

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	results := map[string]*ImportedFile{}
	for _, p := range todo {
		results[p.path] = &ImportedFile{Path: p.path}
	}
	var rows []store.Secret
	for _, name := range names {
		e := entries[name]
		row := store.Secret{Name: name, Principal: ImportPrincipal, Masked: e.masked, Shared: true}
		existing, err := st.GetSecretRow(name, "")
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("read secret %s: %w", name, err)
		}
		if existing != nil {
			if held, oerr := controller.OpenSecretValue(c, store.DefaultTeam, existing); oerr != nil || held != e.value {
				results[e.from].Conflicts = append(results[e.from].Conflicts, name)
			}
			continue
		}
		sealed, err := controller.SealSecretValue(c, store.DefaultTeam, &row, e.value)
		if err != nil {
			return nil, fmt.Errorf("seal secret %s: %w", name, err)
		}
		row.Value = sealed
		rows = append(rows, row)
	}
	var out []ImportedFile
	err := st.ImportSecrets(ctx, rows, func(present []string) (map[string]string, error) {
		raced := map[string]bool{}
		for _, name := range present {
			raced[name] = true
		}
		for _, row := range rows {
			e := entries[row.Name]
			if raced[row.Name] {
				results[e.from].Conflicts = append(results[e.from].Conflicts, row.Name)
				continue
			}
			results[e.from].Imported++
		}
		marks := map[string]string{}
		for _, p := range todo {
			r := results[p.path]
			sort.Strings(r.Conflicts)
			body, err := json.Marshal(legacyMark{SHA256: p.sum, Imported: r.Imported, Conflicts: r.Conflicts})
			if err != nil {
				return nil, err
			}
			marks[legacyMarkPrefix+p.path] = string(body)
			out = append(out, *r)
		}
		return marks, nil
	}, now)
	if err != nil {
		return nil, fmt.Errorf("import local secrets: %w", err)
	}
	return out, nil
}

// TakeLegacyReport returns the import of each of files that nobody has been
// told about yet and records that they now have, so each file's notice is
// printed once however many processes ask.
func TakeLegacyReport(ctx context.Context, st *store.Store, files LegacyFiles, now time.Time) ([]ImportedFile, error) {
	var out []ImportedFile
	marks := map[string]string{}
	for _, path := range []string{files.Secrets, files.Config} {
		if path == "" {
			continue
		}
		mark, found, err := readMark(ctx, st, path)
		if err != nil {
			return nil, err
		}
		if !found || mark.Reported {
			continue
		}
		mark.Reported = true
		body, err := json.Marshal(mark)
		if err != nil {
			return nil, err
		}
		marks[legacyMarkPrefix+path] = string(body)
		out = append(out, ImportedFile{Path: path, Imported: mark.Imported, Conflicts: mark.Conflicts})
	}
	if len(marks) == 0 {
		return nil, nil
	}
	if err := st.SetImportMarks(ctx, marks, now); err != nil {
		return nil, fmt.Errorf("record the local secrets import notice: %w", err)
	}
	return out, nil
}

func readMark(ctx context.Context, st *store.Store, path string) (legacyMark, bool, error) {
	raw, found, err := st.ImportMark(ctx, legacyMarkPrefix+path)
	if err != nil {
		return legacyMark{}, false, err
	}
	if !found {
		return legacyMark{}, false, nil
	}
	var mark legacyMark
	// safety: a record this build cannot read counts as no record, so the file
	// imports again, which only adds names the store does not hold.
	decodeErr := json.Unmarshal([]byte(raw), &mark)
	return mark, decodeErr == nil, nil
}

func parseDotenv(path string, data []byte) (map[string]string, error) {
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
