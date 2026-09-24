package bincache

import "os"

// safety: Windows has only a per-user temporary key root to trust.
func ownedByThisUser(os.FileInfo) bool { return true }
