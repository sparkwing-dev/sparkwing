package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func validateFleetSourceOwnership(root, bundle, sha string) error {
	if root == "" || bundle == "" || sha == "" {
		return errors.New("exact source handoff is incomplete; start this pipeline through sparkwing run --sw-fleet")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absTemp, err := filepath.Abs(os.TempDir())
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absTemp, absRoot)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || !strings.HasPrefix(filepath.Base(absRoot), "sparkwing-worktree-snapshot-") {
		return errors.New("exact source root is outside Sparkwing's private temporary namespace")
	}
	info, err := os.Lstat(absRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("exact source root is not a private materialized directory")
	}
	wantBundle := filepath.Join(absRoot, "snapshot.bundle")
	absBundle, err := filepath.Abs(bundle)
	if err != nil || absBundle != wantBundle {
		return errors.New("exact source bundle is not bound to its materialized root")
	}
	marker, err := os.ReadFile(filepath.Join(absRoot, ".sparkwing-fleet-owned"))
	if err != nil || strings.TrimSpace(string(marker)) != sha {
		return errors.New("exact source ownership marker does not match its commit")
	}
	return nil
}

func cleanupFleetSource(root, sha string) {
	if err := validateFleetSourceOwnership(root, filepath.Join(root, "snapshot.bundle"), sha); err != nil {
		fmt.Fprintf(os.Stderr, "warn: fleet source cleanup refused: %v\n", err)
		return
	}
	if err := os.RemoveAll(root); err != nil {
		fmt.Fprintf(os.Stderr, "warn: fleet source cleanup failed: %v\n", err)
	}
}
