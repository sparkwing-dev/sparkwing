package crons

import (
	"io"
	"os"
	"strings"
)

const preparedPinPrefix = "sparkwing-prepared-entry-v1\n"

// PreparedPinIdentity reads a prepared entry receipt; executable pins return empty.
func PreparedPinIdentity(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	body, err := io.ReadAll(io.LimitReader(file, int64(len(preparedPinPrefix)+65)))
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(string(body), preparedPinPrefix) {
		return "", nil
	}
	return strings.TrimPrefix(string(body), preparedPinPrefix), nil
}
