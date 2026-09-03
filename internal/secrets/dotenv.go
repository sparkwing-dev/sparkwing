package secrets

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func DefaultDotenvPath() (string, error) {
	return fssecure.ConfigFile("secrets.env")
}

func DefaultConfigPath() (string, error) {
	return fssecure.ConfigFile("config.env")
}

type DotenvSource struct {
	secretsPath string
	configPath  string

	once    sync.Once
	mu      sync.RWMutex
	masked  map[string]string
	plain   map[string]string
	loadErr error
}

func NewDotenvSource(secretsPath string) *DotenvSource {
	return &DotenvSource{secretsPath: secretsPath}
}

func NewDotenvSourcePaths(secretsPath, configPath string) *DotenvSource {
	return &DotenvSource{secretsPath: secretsPath, configPath: configPath}
}

var ErrSecretMissing = sparkwing.ErrSecretMissing

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

func (s *DotenvSource) SecretsPath() string {
	if s.secretsPath != "" {
		return s.secretsPath
	}
	p, _ := DefaultDotenvPath()
	return p
}

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
		out[key] = unquoteDotenvValue(strings.TrimSpace(line[eq+1:]))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}

func unquoteDotenvValue(val string) string {
	if len(val) < 2 {
		return val
	}
	switch {
	case val[0] == '"' && val[len(val)-1] == '"':
		if unquoted, err := strconv.Unquote(val); err == nil {
			return unquoted
		}
		// hack: a hand-written file can hold quoting this cannot decode, such
		// as a Windows path, and its literal text serves its author better than
		// an error that hides every other entry in the file.
		return val[1 : len(val)-1]
	case val[0] == '\'' && val[len(val)-1] == '\'':
		return val[1 : len(val)-1]
	}
	return val
}

func WriteDotenvEntry(path, name, value string) error {
	if path == "" {
		p, err := DefaultDotenvPath()
		if err != nil {
			return err
		}
		path = p
	}
	if err := fssecure.EnsureConfigDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("prepare %s: %w", filepath.Dir(path), err)
	}
	existing, err := parseDotenvFile(path)
	if err != nil {
		return err
	}
	existing[name] = value
	return writeDotenvFile(path, existing)
}

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
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sortStrings(keys)
	var b strings.Builder
	// safety: quoting every value keeps write and read exact inverses; a value
	// quoted only when it looks like it needs quoting is indistinguishable on
	// read from one whose own text begins and ends with a quote.
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%q\n", k, data[k])
	}
	if err := fssecure.WriteFile(path, []byte(b.String())); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}
