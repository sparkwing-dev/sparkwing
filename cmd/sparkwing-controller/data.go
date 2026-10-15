package main

import (
	"crypto/rand"
	"fmt"
	"net"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
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
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return blob.NewFilesystem(blob.FilesystemOptions{Directory: filepath.Join(root, "data"), Endpoint: endpoint, SigningKey: key})
}
