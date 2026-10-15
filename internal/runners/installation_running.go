package runners

import (
	"errors"
	"fmt"
	"os"
)

func replaceRunningRunner(source, target string, move func(string, string) error, remove func(string) error) error {
	backup := target + ".old"
	if err := remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove prior runner image %s: %w", backup, err)
	}
	// safety: Windows cannot overwrite a running image, but can rename it before publication.
	if err := move(target, backup); err != nil {
		return fmt.Errorf("retain running image at %s: %w", backup, err)
	}
	if err := move(source, target); err != nil {
		if rollbackErr := move(backup, target); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("restore runner from %s: %w", backup, rollbackErr))
		}
		return err
	}
	return nil
}
