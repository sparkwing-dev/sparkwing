package agentconfig

import (
	"errors"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

// hack: temporary, and deleted with userconfig's legacy migration.
func init() {
	userconfig.RegisterLegacyValidator(userconfig.Agent, func(section *yaml.Node) error {
		if carriesEnrolledKey(section, 0) {
			return errors.New(EnrolledModeRemoved)
		}
		var cfg Config
		if err := userconfig.DecodeStrict(section, &cfg); err != nil {
			return err
		}
		_, err := Validate(cfg)
		return err
	})
}

// hack: a runner service installed before config.yaml passes its own
// agent.yaml to --config, and a service keeps that path across an upgrade, so
// a file whose top level holds the agent's controller key loads whole as the
// agent section. It reports false for any other file.
func loadLegacyFile(path string) (*Config, bool, error) {
	f, err := fssecure.OpenPrivateConfig(path)
	if err != nil {
		return nil, false, nil
	}
	defer func() { _ = f.Close() }()
	dec := yaml.NewDecoder(f)
	var doc, trailing yaml.Node
	if err := dec.Decode(&doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, false, nil
	}
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, true, fmt.Errorf("parse %s: multiple YAML documents are not allowed", path)
	}
	root := doc.Content[0]
	legacy := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		legacy = legacy || root.Content[i].Value == "controller"
	}
	if !legacy {
		return nil, false, nil
	}
	if carriesEnrolledKey(root, 0) {
		return nil, true, fmt.Errorf("parse %s: %s", path, EnrolledModeRemoved)
	}
	var cfg Config
	if err := userconfig.DecodeStrict(root, &cfg); err != nil {
		return nil, true, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, true, nil
}
