package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func writeAgentConfig(path string, cfg agentFileConfig, force bool) (rollback func() error, retErr error) {
	if err := validateAgentFileConfig(cfg); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("runner credential is empty")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	var section yaml.Node
	var previous map[string]any
	var directory string
	rollback = func() error { return rollbackRunnerConfig(path, directory, previous) }
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, rollback())
			rollback = nil
		}
	}()
	err = userconfig.Update(path, userconfig.Runner, "the runner config", &section, func(found bool) error {
		if found && !force {
			return errAgentSectionExists(path)
		}
		if found {
			// safety: decoded values resolve aliases while retaining explicit empty collections.
			if err := section.Decode(&previous); err != nil {
				return err
			}
		}
		var err error
		directory, err = fssecure.MkdirPrivateTemp(filepath.Dir(path), "runner-credentials-")
		if err != nil {
			return err
		}
		if err := fssecure.WriteFile(filepath.Join(directory, "agent-token"), []byte(cfg.Token)); err != nil {
			return err
		}
		cfg.CredentialsDir = directory
		section = yaml.Node{}
		return section.Encode(cfg)
	})
	return rollback, err
}

func rollbackRunnerConfig(path, directory string, previous map[string]any) error {
	if directory == "" {
		return nil
	}
	replaced := errors.New("runner configuration belongs to another enrollment")
	var section yaml.Node
	err := userconfig.Update(path, userconfig.Runner, "the runner config", &section, func(found bool) error {
		current := userconfig.YAMLField(&section, "credentials_dir")
		// safety: another enrollment or settings edit must survive this enrollment's failure.
		if !found || current == nil || current.Value != directory {
			return replaced
		}
		section = yaml.Node{}
		if previous == nil {
			return nil
		}
		return section.Encode(previous)
	})
	if errors.Is(err, replaced) {
		err = nil
	}
	for _, name := range []string{filepath.Join(directory, "agent-token"), directory} {
		if removeErr := os.Remove(name); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}
	return err
}
