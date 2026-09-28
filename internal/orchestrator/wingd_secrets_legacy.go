package orchestrator

// hack: the daemon's import of the local dotenv secret files. A later release
// deletes this file with internal/localsecrets/legacy_migrate.go.

import (
	"context"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/localsecrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (a *wingdAPI) importLegacySecrets(ctx context.Context, rw *store.Store, cipher *localsecrets.Cipher) error {
	files, err := localsecrets.FindLegacyFiles()
	if err != nil {
		return err
	}
	result, err := localsecrets.ImportLegacy(ctx, rw, cipher, files, time.Now())
	if err != nil || result == nil {
		return err
	}
	a.logger.Info("imported local dotenv secrets; the files are no longer read and can be deleted",
		"secrets_file", files.Secrets, "config_file", files.Config,
		"imported", strings.Join(result.Imported, ","), "kept_store_value", strings.Join(result.Conflicts, ","))
	return nil
}
