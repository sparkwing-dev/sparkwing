package crons

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const preparedPinName = "prepared-entry"

const preparedPinPrefix = "sparkwing-prepared-entry-v1\n"

// PreparedPinIdentity reads a prepared entry receipt; executable pins return empty.
func PreparedPinIdentity(path string) (string, error) {
	if filepath.Base(path) != preparedPinName {
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
	if !strings.HasPrefix(string(body), preparedPinPrefix) || len(body) != len(preparedPinPrefix)+64 {
		return "", errors.New("prepared schedule pin is malformed")
	}
	return strings.TrimPrefix(string(body), preparedPinPrefix), nil
}
