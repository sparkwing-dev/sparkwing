package secrets

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// DefaultDotenvPath returns the location of the laptop's masked
// secret store. The file is created on first write; readers must
// tolerate ENOENT as "no secrets stored locally yet" rather than an
// error.
func DefaultDotenvPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	return filepath.Join(home, ".config", "sparkwing", "secrets.env"), nil
}

// DefaultConfigPath returns the location of the laptop's plain
// (non-masked) config store. Same directory as the secrets file but
// distinct file so an operator can chmod / share / version-control
// either independently of the other.
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	return filepath.Join(home, ".config", "sparkwing", "config.env"), nil
}

// DotenvSource resolves secrets from one or two flat KEY=VAL files.
// Two files because splits storage by mask intent:
type DotenvSource struct {
	secretsPath string
	configPath  string

	once    sync.Once
	mu      sync.RWMutex
	masked  map[string]string
	plain   map[string]string
	loadErr error
}

// NewDotenvSource returns a source backed by the default file pair
// under ~/.config/sparkwing/. Tests use NewDotenvSourcePaths to
// pin the file locations explicitly.
func NewDotenvSource(secretsPath string) *DotenvSource {
	return &DotenvSource{secretsPath: secretsPath}
}

// NewDotenvSourcePaths returns a source backed by an explicit
// (secrets, config) file pair. Either may be empty to fall back to
// the default location for that file.
func NewDotenvSourcePaths(secretsPath, configPath string) *DotenvSource {
	return &DotenvSource{secretsPath: secretsPath, configPath: configPath}
}

// ErrSecretMissing means the source has no entry for this name.
// Re-exports the canonical sparkwing.ErrSecretMissing so existing
// callers that errors.Is against this name keep working without
// importing sparkwing directly.
var ErrSecretMissing = sparkwing.ErrSecretMissing

// Read returns the value + masked flag for `name`, or ErrSecretMissing
// when neither file holds an entry. The first call lazy-loads both
// files; subsequent calls hit the cached maps. Plain (config.env)
// wins over masked (secrets.env) on collision -- the explicit-plain
// intent beats the safe default.
func (s *DotenvSource) Read(name string) (string, bool, error) {
	s.once.Do(s.load)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.loadErr != nil {
		return "", false, s.loadErr
	}
	if v, ok := s.plain[name]; ok {
		return v, false, nil
	}
	if v, ok := s.masked[name]; ok {
		return v, true, nil
	}
	return "", false, ErrSecretMissing
}

// Path returns the masked-side file path. Kept for backwards-compat
// with callers that wrote single-file error messages; new
// code should prefer SecretsPath / ConfigPath.
func (s *DotenvSource) Path() string { return s.SecretsPath() }

// SecretsPath returns the resolved secrets.env path.
func (s *DotenvSource) SecretsPath() string {
	if s.secretsPath != "" {
		return s.secretsPath
	}
	p, _ := DefaultDotenvPath()
	return p
}

// ConfigPath returns the resolved config.env path.
func (s *DotenvSource) ConfigPath() string {
	if s.configPath != "" {
		return s.configPath
	}
	p, _ := DefaultConfigPath()
	return p
}

func (s *DotenvSource) load() {
	masked, mErr := parseDotenvFile(s.SecretsPath())
	plain, pErr := parseDotenvFile(s.ConfigPath())
	s.mu.Lock()
	s.masked = masked
	s.plain = plain
	switch {
	case mErr != nil:
		s.loadErr = mErr
	case pErr != nil:
		s.loadErr = pErr
	}
	s.mu.Unlock()
}

func parseDotenvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Treat a missing file as an empty source. First-time
			// laptop callers haven't created it yet; resolution
			// errors will surface as ErrSecretMissing rather than
			// "file not found" which is more actionable.
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("%s:%d: malformed line, want KEY=VALUE", path, lineNo)
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		// Strip a single layer of surrounding quotes so writers can
		// quote values that contain spaces / special chars.
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') ||
				(val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		out[key] = val
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}

// WriteDotenvEntry upserts (name, value) into the dotenv file at path.
// Creates the directory and file if needed; chmods the file 0600 on
// every write so accidental world-readability is corrected. An empty
// path resolves to DefaultDotenvPath.
func WriteDotenvEntry(path, name, value string) error {
	if path == "" {
		p, err := DefaultDotenvPath()
		if err != nil {
			return err
		}
		path = p
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	existing, err := parseDotenvFile(path)
	if err != nil {
		return err
	}
	existing[name] = value
	return writeDotenvFile(path, existing)
}

// DeleteDotenvEntry removes name from the dotenv file. Returns
// ErrSecretMissing when name wasn't present so the CLI can render a
// distinct message vs an actual write error.
func DeleteDotenvEntry(path, name string) error {
	if path == "" {
		p, err := DefaultDotenvPath()
		if err != nil {
			return err
		}
		path = p
	}
	existing, err := parseDotenvFile(path)
	if err != nil {
		return err
	}
	if _, ok := existing[name]; !ok {
		return ErrSecretMissing
	}
	delete(existing, name)
	return writeDotenvFile(path, existing)
}

// ListDotenvEntries returns all names in the file (values blanked at
// the call site if the caller wants a safe-to-render projection).
// Returns an empty slice when the file doesn't exist yet.
func ListDotenvEntries(path string) (map[string]string, error) {
	if path == "" {
		p, err := DefaultDotenvPath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	return parseDotenvFile(path)
}

func writeDotenvFile(path string, data map[string]string) error {
	// Sort keys so re-reads + version-control diffs are stable.
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	// strings.Slice import would be heavier; use a tiny inline sort.
	sortStrings(keys)
	var b strings.Builder
	for _, k := range keys {
		v := data[k]
		// Quote values that contain whitespace, '=', or '#' so a
		// reader with a stricter parser won't trip. Values without
		// those round-trip cleanly without quotes.
		needsQuote := strings.ContainsAny(v, " \t=#\n")
		if needsQuote {
			fmt.Fprintf(&b, "%s=%q\n", k, v)
		} else {
			fmt.Fprintf(&b, "%s=%s\n", k, v)
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod %s 0600: %w", path, err)
	}
	return nil
}

// sortStrings is a tiny insertion sort to avoid pulling sort into
// this leaf package's import set. Inputs are <50 entries in the
// realistic case.
func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}
