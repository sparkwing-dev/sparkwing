package runners

import (
	"context"
	"errors"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/credentials"
)

// FileBearer reads agent-token once and retains it in a controller transport callback.
func FileBearer(directory string) (func(context.Context) (string, error), error) {
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("runner credentials_dir is required")
	}
	dir, err := credentials.Open(directory)
	if err != nil {
		return nil, err
	}
	raw, err := dir.Read("agent-token")
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, errors.New("runner credential file is empty")
	}
	return func(ctx context.Context) (string, error) {
		if err := context.Cause(ctx); err != nil {
			return "", err
		}
		return raw, nil
	}, nil
}
