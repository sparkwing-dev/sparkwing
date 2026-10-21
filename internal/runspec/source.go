package runspec

import (
	"encoding/hex"
	"strings"
)

// IsCommitID reports whether value is a canonical lowercase SHA-1 or SHA-256 object ID.
func IsCommitID(value string) bool {
	if (len(value) != 40 && len(value) != 64) || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
