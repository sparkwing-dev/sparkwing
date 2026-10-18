package bincache

// SeedRef names the bundle ref that carries commit sha, either the snapshot or a selected code revision.
func SeedRef(sha string) string { return "refs/sparkwing-seed/" + sha }
