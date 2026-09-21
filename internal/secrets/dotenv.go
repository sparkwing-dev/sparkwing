package secrets

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/configguard"
	"github.com/sparkwing-dev/sparkwing/internal/dotenv"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	// SecretsPathEnv names the masked local store, the way SPARKWING_PROFILES
	// names profiles.yaml. SPARKWING_HOME does not move the file.
	SecretsPathEnv = "SPARKWING_SECRETS"

	// ConfigPathEnv names the plain local store, the one `secret set --plain`
	// writes.
	ConfigPathEnv = "SPARKWING_CONFIG_ENV"
)

// DefaultDotenvPath reports the masked local store: $SPARKWING_SECRETS when
// set, else secrets.env in [fssecure.ConfigDir].
func DefaultDotenvPath() (string, error) {
	if v := os.Getenv(SecretsPathEnv); v != "" {
		return v, nil
	}
	return fssecure.ConfigFile("secrets.env")
}

// DefaultConfigPath reports the plain local store: $SPARKWING_CONFIG_ENV when
// set, else config.env in [fssecure.ConfigDir].
func DefaultConfigPath() (string, error) {
	if v := os.Getenv(ConfigPathEnv); v != "" {
		return v, nil
	}
	return fssecure.ConfigFile("config.env")
}

// safety: a command under a scratch home is expected to stay there, and a
// secret written into the operator's real store is found only by accident.
// Which variable moves this file depends on which of the two stores it is.
func guardSandboxWrite(path string) error {
	override := SecretsPathEnv
	if plain, err := DefaultConfigPath(); err == nil && plain == path {
		override = ConfigPathEnv
	}
	return configguard.GuardWrite("the local secret store", override, path)
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

func WriteDotenvEntry(path, name, value string) error {
	if path == "" {
		p, err := DefaultDotenvPath()
		if err != nil {
			return err
		}
		path = p
	}
	if err := guardSandboxWrite(path); err != nil {
		return err
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
	if err := guardSandboxWrite(path); err != nil {
		return err
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
