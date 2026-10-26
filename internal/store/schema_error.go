package store

import "fmt"

// path is the SQLite file, or empty for PostgreSQL.
func schemaMismatchError(version int, path string) error {
	if version > expectedSchemaVersion {
		return fmt.Errorf("sparkwing: store schema version %d is newer than this binary (expects %d); start a binary matching the store, or restore a backup matching this binary into a separate database", version, expectedSchemaVersion)
	}
	remedy := "drop the database"
	if path != "" {
		remedy = "delete " + path
	}
	return fmt.Errorf("sparkwing: store schema version %d is older than this binary (expects %d); back up the store and use a binary matching it; this build cannot upgrade it; only for a disposable store, %s and start again", version, expectedSchemaVersion, remedy)
}

// safety: v0.68 replaced the store without an import, so a pre-rewrite store
// gets the move-aside remedy instead of advice to find a matching binary.
func preRewriteStoreError(version int, path string) error {
	const lead = "sparkwing: %s is a store from Sparkwing v0.67 or earlier (schema version %d); this release starts a new store and cannot read or import it, and left the old one unchanged; "
	if path == "" {
		return fmt.Errorf(lead+"back up this database, then point the server at an empty database", "this database", version)
	}
	return fmt.Errorf(lead+"move it aside and run again: mv %s %s.v067 (move %s-wal and %s-shm with it if they exist)", path, version, path, path, path, path)
}
