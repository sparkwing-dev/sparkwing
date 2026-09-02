package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type nodeManifestReader interface {
	GetNode(ctx context.Context, runID, nodeID string) (*store.Node, error)
}

func stageConsumedArtifacts(ctx context.Context, store storage.ArtifactStore, state nodeManifestReader, runID, workspace string, edges []sparkwing.ConsumeEdge) (int, error) {
	staged := 0
	var total int64
	if len(edges) == 0 {
		return staged, nil
	}
	// safety: every staged path is resolved through the workspace root, so a
	// symlink planted by an earlier node cannot redirect a write out of it.
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return staged, err
	}
	defer func() { _ = root.Close() }()

	for _, e := range edges {
		node, err := state.GetNode(ctx, runID, e.Producer)
		if err != nil {
			return staged, fmt.Errorf("producer %q: %w", e.Producer, err)
		}
		if node == nil || node.ArtifactManifest == "" {
			continue
		}
		manifest, err := fetchManifest(ctx, store, node.ArtifactManifest)
		if err != nil {
			return staged, fmt.Errorf("producer %q manifest: %w", e.Producer, err)
		}
		for _, entry := range manifest.Entries {
			rel, err := stageRel(e.Into, entry.Path)
			if err != nil {
				return staged, fmt.Errorf("producer %q: %w", e.Producer, err)
			}
			n, err := stageBlob(ctx, store, root, entry, rel, maxStagedTotalBytes-total)
			total += n
			if err != nil {
				return staged, fmt.Errorf("producer %q artifact %q: %w", e.Producer, entry.Path, err)
			}
			staged++
		}
	}
	return staged, nil
}

func fetchManifest(ctx context.Context, store storage.ArtifactStore, digest string) (artifactManifest, error) {
	rc, err := store.Get(ctx, artifactManifestKey(digest))
	if err != nil {
		return artifactManifest{}, err
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		return artifactManifest{}, err
	}
	var m artifactManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return artifactManifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	return m, nil
}

var (
	maxStagedArtifactBytes = int64(8 << 30)
	maxStagedTotalBytes    = int64(32 << 30)
)

func stageBlob(ctx context.Context, store storage.ArtifactStore, root *os.Root, entry artifactEntry, rel string, remaining int64) (int64, error) {
	rc, err := store.Get(ctx, artifactBlobKey(entry.Digest))
	if err != nil {
		return 0, err
	}
	defer func() { _ = rc.Close() }()
	if parent := filepath.Dir(rel); parent != "." {
		if err := root.MkdirAll(parent, 0o755); err != nil {
			return 0, err
		}
	}
	mode := os.FileMode(entry.Mode).Perm()
	// safety: replace an existing destination instead of opening through it,
	// so a symlink left in the workspace cannot capture the write.
	_ = root.Remove(rel)
	f, err := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return 0, err
	}
	limit := min(maxStagedArtifactBytes, remaining)
	n, err := io.Copy(f, io.LimitReader(rc, limit+1))
	if err != nil {
		_ = f.Close()
		return n, err
	}
	if n > limit {
		_ = f.Close()
		if n > maxStagedArtifactBytes {
			return n, fmt.Errorf("blob exceeds the staging size limit of %d bytes", maxStagedArtifactBytes)
		}
		return n, fmt.Errorf("staged artifacts exceed the total staging limit of %d bytes", maxStagedTotalBytes)
	}
	if err := f.Close(); err != nil {
		return n, err
	}
	return n, root.Chmod(rel, mode)
}

func stageRel(into, relPath string) (string, error) {
	into = filepath.FromSlash(into)
	relPath = filepath.FromSlash(relPath)
	if filepath.IsAbs(into) || filepath.IsAbs(relPath) {
		return "", fmt.Errorf("staged path %q must be relative to the workspace", relPath)
	}
	rel := filepath.Clean(filepath.Join(into, relPath))
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("staged path %q escapes workspace", rel)
	}
	return rel, nil
}
