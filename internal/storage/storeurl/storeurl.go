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

// MeasurementURL is a validated filesystem or S3 measurement location.
type MeasurementURL struct {
	scheme, path, bucket, prefix string
}

// ParseMeasurementURL validates a measurement location without opening storage.
func ParseMeasurementURL(raw string) (MeasurementURL, error) {
	scheme, rest, err := splitScheme(raw)
	if err != nil {
		return MeasurementURL{}, err
	}
	cfg := MeasurementURL{scheme: scheme}
	switch scheme {
	case "fs":
		cfg.path, err = fsPath(rest)
	case "s3":
		cfg.bucket, cfg.prefix, err = s3store.ParseURL(raw)
	default:
		err = fmt.Errorf("storeurl: unsupported scheme %q in %q", scheme, raw)
	}
	return cfg, err
}

// Open opens usage reporting with a bounded listing budget.
func (u MeasurementURL) Open(ctx context.Context, maxPages int) (storage.UsageReporter, error) {
	switch u.scheme {
	case "fs":
		return fs.NewUsageReporter(u.path)
	case "s3":
		client, err := s3store.NewClient(ctx)
		if err != nil {
			return nil, err
		}
		store := s3store.NewMeasurementStore(u.bucket, u.prefix, client)
		store.MaxUsagePages = maxPages
		return store, nil
	default:
		return nil, errors.New("storeurl: measurement URL is not configured")
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
