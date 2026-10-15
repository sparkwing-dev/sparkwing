package store

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// safety: source binding measures its window from commit so upload time cannot consume it.
const sourceBundleRetention = 24 * time.Hour

// ErrSourceAlreadyBound requires a new source upload for another run.
var ErrSourceAlreadyBound = errors.New("source bundle already used or expired; upload again")

func validUploadKey(key string) bool {
	if key == "" || len(key) > 900 || strings.HasPrefix(key, "/") {
		return false
	}
	for _, c := range key {
		if c == '\\' || c < 0x20 || c == 0x7f {
			return false
		}
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return strings.HasPrefix(key, "bin/") || strings.HasPrefix(key, "artifacts/") || strings.HasPrefix(key, "sources/") ||
		strings.HasPrefix(key, "outputs/")
}

func validSHA256(raw string) bool {
	if len(raw) != 64 || strings.ToLower(raw) != raw {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

// SourceKeyDigest accepts one submission's immutable source object key.
func SourceKeyDigest(key string) (string, bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 3 || parts[0] != "sources" || !validSHA256(parts[1]) || len(parts[2]) != 32 {
		return "", false
	}
	if _, err := hex.DecodeString(parts[2]); err != nil || strings.ToLower(parts[2]) != parts[2] {
		return "", false
	}
	return parts[1], true
}
