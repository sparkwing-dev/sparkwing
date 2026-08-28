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
			dest, err := stageDest(workspace, e.Into, entry.Path)
			if err != nil {
				return staged, fmt.Errorf("producer %q: %w", e.Producer, err)
			}
			if err := stageBlob(ctx, store, entry, dest); err != nil {
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

func stageBlob(ctx context.Context, store storage.ArtifactStore, entry artifactEntry, dest string) error {
	rc, err := store.Get(ctx, artifactBlobKey(entry.Digest))
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(entry.Mode)
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(dest, mode)
}

func stageDest(workspace, into, relPath string) (string, error) {
	into = filepath.FromSlash(into)
	relPath = filepath.FromSlash(relPath)
	if filepath.IsAbs(into) || filepath.IsAbs(relPath) {
		return "", fmt.Errorf("staged path %q must be relative to the workspace", relPath)
	}
	rel := filepath.Join(into, relPath)
	dest := filepath.Join(workspace, rel)
	clean := filepath.Clean(workspace)
	if dest != clean && !strings.HasPrefix(dest, clean+string(os.PathSeparator)) {
		return "", fmt.Errorf("staged path %q escapes workspace", rel)
	}
	return dest, nil
}
