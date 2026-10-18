package runspec

import "encoding/hex"

// IsCommitID reports whether value is a full SHA-1 or SHA-256 object ID.
func IsCommitID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
