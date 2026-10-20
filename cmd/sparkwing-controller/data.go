package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	s3store "github.com/sparkwing-dev/sparkwing/internal/storage/s3"
)

func openFilesystemData(root, externalURL, address string) (*blob.Filesystem, error) {
	endpoint := externalURL
	if endpoint == "" {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("filesystem data address: %w", err)
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "localhost"
		}
		endpoint = "http://" + net.JoinHostPort(host, port)
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
