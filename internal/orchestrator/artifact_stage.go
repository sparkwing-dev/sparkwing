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

// nodeManifestReader is the slice of state a consumer needs to resolve a
// producer's recorded artifact manifest. [StateBackend] satisfies it.
type nodeManifestReader interface {
	GetNode(ctx context.Context, runID, nodeID string) (*store.Node, error)
}

// stageConsumedArtifacts materializes, into workspace, the artifacts of
// every producer this consumer declared via Consumes. For each edge it
// reads the producer node's recorded manifest digest from state, fetches
// the manifest and each file blob from store, and writes the bytes at the
// producer's declared relative path -- or under the edge's Into prefix
// when set, with the producer's internal structure preserved -- creating
// parent directories and overwriting any existing file. When two edges
// resolve to the same destination, the later edge wins (declaration
// order), matching the plan-time overlap warning.
//
// A producer whose node recorded no manifest (declared no outputs, or has
// not finished) contributes nothing; staging that edge is a no-op rather
// than an error. store and state must be non-nil; the caller skips
// staging when no artifact store is configured. Returns the number of
// files written.
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

// fetchManifest reads and decodes the manifest stored under digest.
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

// stageBlob fetches the entry's content-addressed bytes and writes them
// at dest with the recorded mode, creating parent directories and
// truncating any existing file. Chmod runs after the write so an existing
// file's mode is corrected on overwrite, not only on create.
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

// stageDest joins workspace, the optional Into prefix, and the producer's
// relative path into an absolute destination, and rejects a result that
// escapes the workspace (a malformed Into prefix or manifest path). An
// absolute Into or manifest path is rejected outright rather than silently
// neutralized: filepath.Join would otherwise root it back under workspace,
// hiding malformed input instead of surfacing it.
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
