// Package projectconfig owns checkout-local pipeline declarations.
package projectconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/sparks"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// Filename is the project configuration file inside .sparkwing.
const Filename = "sparkwing.yaml"

// ErrTooLarge refuses a configuration above limits.ProjectConfigBytes.
var ErrTooLarge = fmt.Errorf("%s is larger than the %d MiB limit", Filename, limits.ProjectConfigBytes>>20)

// Parse validates checkout declarations and preserves explicit empty values.
func Parse(raw []byte) (*Config, error) {
	if int64(len(raw)) > limits.ProjectConfigBytes {
		return nil, ErrTooLarge
	}
	var cfg Config
	doc, err := userconfig.DecodeYAML(Filename, raw, &cfg)
	if err != nil {
		return nil, err
	}
	if err := Validate(cfg); err != nil {
		return nil, userconfig.LocateError(Filename, doc, err)
	}
	return &cfg, nil
}

// Load returns nil when the project file is absent.
func Load(path string) (*Config, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	raw, err := io.ReadAll(io.LimitReader(file, limits.ProjectConfigBytes+1))
	if err = errors.Join(err, file.Close()); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	cfg, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Discover searches upward for checkout declarations without reading machine settings.
func Discover(startDir string) (string, *Config, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", nil, err
	}
	for {
		path := filepath.Join(dir, ".sparkwing", Filename)
		cfg, err := Load(path)
		if err != nil || cfg != nil {
			return path, cfg, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil, nil
		}
		dir = parent
	}
}

// LoadSparksManifest reads the sparks section of
// <sparkwingDir>/sparkwing.yaml as a *sparks.Manifest. Returns (nil, nil)
// when the file is absent or declares no sparks.
func LoadSparksManifest(sparkwingDir string) (*sparks.Manifest, error) {
	if sparkwingDir == "" {
		return nil, errors.New("sparks: sparkwingDir must not be empty")
	}
	cfg, err := Load(filepath.Join(sparkwingDir, Filename))
	if err != nil {
		return nil, err
	}
	if cfg == nil || len(cfg.Sparks) == 0 {
		return nil, nil
	}
	libs := make([]sparks.Library, len(cfg.Sparks))
	for i, s := range cfg.Sparks {
		libs[i] = sparks.Library{Name: s.Name, Source: s.Source, Version: s.Version}
	}
	return &sparks.Manifest{Libraries: libs}, nil
}

// WriteSparksSection replaces the sparks section, preserving other sections, comments and key order.
func WriteSparksSection(path string, libs []sparks.Library) error {
	doc, root, err := loadDocument(path)
	if err != nil {
		return err
	}
	idx := mappingIndex(root, "sparks")
	if len(libs) == 0 {
		if idx >= 0 {
			root.Content = append(root.Content[:idx], root.Content[idx+2:]...)
		}
	} else {
		var val yaml.Node
		if eerr := val.Encode(libs); eerr != nil {
			return fmt.Errorf("encode sparks: %w", eerr)
		}
		if idx >= 0 {
			root.Content[idx+1] = &val
		} else {
			root.Content = append(root.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: "sparks"}, &val)
		}
	}
	return writeDocument(path, doc)
}

func loadDocument(path string) (*yaml.Node, *yaml.Node, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	if _, err := Parse(raw); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	var doc yaml.Node
	if len(bytes.TrimSpace(raw)) > 0 {
		if uerr := yaml.Unmarshal(raw, &doc); uerr != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", path, uerr)
		}
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, HeadComment: strings.TrimSpace(string(raw))}}}
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("%s: top-level YAML is not a mapping", path)
	}
	return &doc, doc.Content[0], nil
}

func mappingIndex(node *yaml.Node, key string) int {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func writeDocument(path string, doc *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		_ = enc.Close()
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if _, err := Parse(buf.Bytes()); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
