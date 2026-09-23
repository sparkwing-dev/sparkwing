package bincache

import "os"

// ownedByThisUser trusts the per-user temporary directory on Windows, the
// only key root there.
func ownedByThisUser(os.FileInfo) bool { return true }
