package runners

import (
	"context"
	"errors"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/credentials"
)

// FileBearer reads the private agent-token file once for a controller transport.
// The returned closure retains the credential in memory rather than printable options.
// Machine files require the private-file policy; service projections retain their native layout.
func FileBearer(directory string, machine bool) (func(context.Context) (string, error), error) {
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("runner credentials_dir is required")
	}
	dir, err := credentials.Open(directory)
	if err != nil {
		return nil, err
	}
	read := dir.Read
	if machine {
		read = dir.ReadPrivate
	}
	raw, err := read("agent-token")
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
