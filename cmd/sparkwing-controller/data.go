package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	s3store "github.com/sparkwing-dev/sparkwing/internal/storage/s3"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// safety: the maintenance pass deletes log objects itself, so a missing lifecycle rule only warns once at startup;
// it never refuses an upload and no request path reads bucket rules.
func warnLogLifecycle(ctx context.Context, objects *blob.S3, overrides map[store.Team]int) {
	days := []int{int(limits.FreeLogRetention / (24 * time.Hour)), int(limits.FundedLogRetention / (24 * time.Hour))}
	for _, d := range overrides {
		days = append(days, d)
	}
	slices.Sort(days)
	for _, d := range slices.Compact(days) {
		class := fmt.Sprintf("r%d", d)
		if err := objects.LogClassReady(ctx, class); err != nil {
			slog.Warn("log lifecycle backstop rule missing; log uploads continue and the maintenance pass still deletes them at their retention age", "class", class, "err", err)
		}
	}
}

func openFilesystemData(root, externalURL, address string) (*blob.Filesystem, error) {
	endpoint, err := filesystemDataEndpoint(externalURL, address)
	if err != nil {
		return nil, err
	}
	key, err := dataSigningKey(filepath.Join(root, "data-signing.key"))
	if err != nil {
		return nil, err
	}
	return blob.NewFilesystem(blob.FilesystemOptions{Directory: filepath.Join(root, "data"), Endpoint: endpoint, SigningKey: key})
}

func openS3Data(ctx context.Context, storageURL string) (*blob.S3, error) {
	client, bucket, prefix, err := s3store.Open(ctx, storageURL)
	if err != nil {
		return nil, err
	}
	return blob.NewS3(client, bucket, prefix)
}

// safety: transfer URLs a runner already holds are signed with this key, so
// a restart keeps it rather than invalidating every transfer in flight.
func dataSigningKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != dataSigningKeySize {
			return nil, fmt.Errorf("data signing key %s holds %d bytes, want %d; remove it to have a new one generated", path, len(key), dataSigningKeySize)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("data signing key: %w", err)
	}
	key = make([]byte, dataSigningKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("data signing key: %w", err)
	}
	_, werr := f.Write(key)
	if err := errors.Join(werr, f.Sync(), f.Close()); err != nil {
		return nil, errors.Join(fmt.Errorf("data signing key: %w", err), os.Remove(path))
	}
	return key, nil
}

const dataSigningKeySize = 32

func filesystemDataEndpoint(externalURL, address string) (string, error) {
	endpoint := externalURL
	if endpoint == "" {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return "", fmt.Errorf("filesystem data --addr: %w", err)
		}
		if host == "" || (net.ParseIP(host) != nil && net.ParseIP(host).IsUnspecified()) {
			return "", errors.New("filesystem data on a wildcard listener requires --external-url reachable by runners")
		}
		endpoint = "http://" + net.JoinHostPort(host, port)
	}
	return endpoint, nil
}
