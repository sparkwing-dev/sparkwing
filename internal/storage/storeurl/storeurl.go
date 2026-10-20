// Package storeurl opens read-only storage measurements.
package storeurl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/storage"
	"github.com/sparkwing-dev/sparkwing/internal/storage/fs"
	s3store "github.com/sparkwing-dev/sparkwing/internal/storage/s3"
)

// OpenMeasurementStore opens filesystem or S3 usage reporting with a bounded listing budget.
func OpenMeasurementStore(ctx context.Context, raw string, maxPages int) (storage.UsageReporter, error) {
	scheme, rest, err := splitScheme(raw)
	if err != nil {
		return nil, err
	}
	switch scheme {
	case "fs":
		path, err := fsPath(rest)
		if err != nil {
			return nil, err
		}
		return fs.NewUsageReporter(path)
	case "s3":
		bucket, prefix, err := s3store.ParseURL("s3://" + rest)
		if err != nil {
			return nil, err
		}
		client, err := s3store.NewClient(ctx)
		if err != nil {
			return nil, err
		}
		store := s3store.NewMeasurementStore(bucket, prefix, client)
		store.MaxUsagePages = maxPages
		return store, nil
	default:
		return nil, fmt.Errorf("storeurl: unsupported scheme %q in %q", scheme, raw)
	}
}

func splitScheme(raw string) (scheme, rest string, err error) {
	if raw == "" {
		return "", "", errors.New("storeurl: empty URL")
	}
	idx := strings.Index(raw, "://")
	if idx < 0 {
		return "", "", fmt.Errorf("storeurl: missing scheme:// in %q", raw)
	}
	return raw[:idx], raw[idx+3:], nil
}

func fsPath(rest string) (string, error) {
	if rest == "" {
		return "", errors.New("storeurl: fs:// requires a path")
	}
	if runtime.GOOS == "windows" {
		if strings.HasPrefix(rest, "/") && filepath.IsAbs(rest[1:]) {
			rest = rest[1:]
		}
		if filepath.IsAbs(rest) {
			return filepath.FromSlash(rest), nil
		}
	}
	if !strings.HasPrefix(rest, "/") && !strings.HasPrefix(rest, "~") {
		return "", fmt.Errorf("storeurl: fs path must be absolute, got %q", rest)
	}
	if strings.HasPrefix(rest, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		rest = home + rest[1:]
	}
	return rest, nil
}
