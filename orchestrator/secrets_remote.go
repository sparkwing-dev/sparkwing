package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/profile"
	"github.com/sparkwing-dev/sparkwing/secrets"
)

// remoteSecretSource builds a secrets.Source backed by the named
// profile's controller (`wing <pipeline> --secrets PROF`).
func remoteSecretSource(profName string) (secrets.Source, error) {
	if profName == "" {
		return nil, errors.New("profile name is required")
	}
	cfgPath, err := profile.DefaultPath()
	if err != nil {
		return nil, err
	}
	cfg, err := profile.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	prof, err := profile.Resolve(cfg, profName)
	if err != nil {
		return nil, fmt.Errorf("resolve profile %q: %w", profName, err)
	}
	if prof.Controller == "" {
		return nil, fmt.Errorf("profile %q has no controller URL", prof.Name)
	}
	c := client.NewWithToken(prof.Controller, nil, prof.Token)
	return secrets.SourceFunc(func(name string) (string, bool, error) {
		sec, gerr := c.GetSecret(context.Background(), name)
		if gerr != nil {
			if errors.Is(gerr, store.ErrNotFound) {
				return "", false, secrets.ErrSecretMissing
			}
			return "", false, gerr
		}
		return sec.Value, sec.Masked, nil
	}), nil
}
