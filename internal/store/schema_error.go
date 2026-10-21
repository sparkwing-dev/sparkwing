package store

import "fmt"

func schemaMismatchError(version int, remedy string) error {
	if version > expectedSchemaVersion {
		return fmt.Errorf("sparkwing: store schema version %d is newer than this binary (expects %d); start a binary matching the store, or restore a backup matching this binary into a separate database", version, expectedSchemaVersion)
	}
	return fmt.Errorf("sparkwing: store schema version %d is older than this binary (expects %d); back up the store and use a binary matching it; this build cannot upgrade it; only for a disposable store, %s and start again", version, expectedSchemaVersion, remedy)
}
