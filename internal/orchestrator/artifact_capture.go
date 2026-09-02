package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type artifactManifest struct {
	Entries []artifactEntry `json:"entries"`
}

type artifactEntry struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Mode   uint32 `json:"mode"`
}

var (
	errArtifactOutsideWorkspace = errors.New("resolves outside the workspace")
	errArtifactUnresolvable     = errors.New("cannot resolve")
)

func artifactBlobKey(digest string) string { return "artifacts/blobs/" + digest }

func artifactManifestKey(digest string) string { return "artifacts/manifests/" + digest }

func captureArtifacts(ctx context.Context, store storage.ArtifactStore, workspace string, globs []string) (string, error) {
	wsRoot, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	root, err := os.OpenRoot(wsRoot)
	if err != nil {
		return "", fmt.Errorf("open workspace: %w", err)
	}
	defer func() { _ = root.Close() }()

	var entries []artifactEntry
	walkErr := filepath.WalkDir(workspace, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if p == workspace {
			return nil
		}
		rel, rerr := filepath.Rel(workspace, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		isSymlink := d.Type()&fs.ModeSymlink != 0
		if d.IsDir() && !isSymlink {
			return nil
		}
		if !anyGlobMatches(globs, rel) {
			return nil
		}
		name, nerr := workspaceRelPath(wsRoot, p)
		if nerr != nil {
			// safety: a wildcard sweep or an outside directory publishes nothing, so skip instead of failing the node
			if errors.Is(nerr, errArtifactOutsideWorkspace) && (!globNamesLiterally(globs, rel) || resolvesToDirectory(p)) {
				sparkwing.LoggerFromContext(ctx).Log("warn", fmt.Sprintf("artifact %q resolves outside the workspace; skipping", rel))
				return nil
			}
			return fmt.Errorf("artifact %q: %w", rel, nerr)
		}
		info, serr := root.Stat(name)
		if serr != nil {
			return fmt.Errorf("artifact %q: %w", rel, serr)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		digest, berr := storeArtifactFile(ctx, store, root, name)
		if berr != nil {
			return fmt.Errorf("artifact %q: %w", rel, berr)
		}
		entries = append(entries, artifactEntry{
			Path:   rel,
			Digest: digest,
			Mode:   uint32(info.Mode().Perm()),
		})
		return nil
	})
	if walkErr != nil {
		return "", walkErr
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	manifestBytes, merr := json.Marshal(artifactManifest{Entries: entries})
	if merr != nil {
		return "", fmt.Errorf("marshal manifest: %w", merr)
	}
	sum := sha256.Sum256(manifestBytes)
	digest := hex.EncodeToString(sum[:])
	if err := putBytes(ctx, store, artifactManifestKey(digest), manifestBytes); err != nil {
		return "", fmt.Errorf("store manifest: %w", err)
	}
	return digest, nil
}

// safety: resolve links before reading a match so a workspace symlink cannot publish a file outside it
// safety: resolve failures return a fixed error so failure text never carries a path outside the workspace
func workspaceRelPath(wsRoot, p string) (string, error) {
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", errArtifactUnresolvable
	}
	rel, err := filepath.Rel(wsRoot, resolved)
	if err != nil {
		return "", errArtifactUnresolvable
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errArtifactOutsideWorkspace
	}
	return rel, nil
}

func globNamesLiterally(globs []string, rel string) bool {
	for _, g := range globs {
		if filepath.ToSlash(g) == rel && !strings.ContainsAny(g, "*?[") {
			return true
		}
	}
	return false
}

func resolvesToDirectory(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// safety: one open serves both the hash and the upload so the stored bytes are the bytes the digest names
func storeArtifactFile(ctx context.Context, store storage.ArtifactStore, root *os.Root, name string) (string, error) {
	f, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	key := artifactBlobKey(digest)
	if ok, herr := store.Has(ctx, key); herr == nil && ok {
		return digest, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if err := store.Put(ctx, key, f); err != nil {
		return "", err
	}
	return digest, nil
}

func putBytes(ctx context.Context, store storage.ArtifactStore, key string, b []byte) error {
	if ok, err := store.Has(ctx, key); err == nil && ok {
		return nil
	}
	return store.Put(ctx, key, strings.NewReader(string(b)))
}

func anyGlobMatches(globs []string, rel string) bool {
	segs := strings.Split(rel, "/")
	for i := len(segs); i >= 1; i-- {
		prefix := strings.Join(segs[:i], "/")
		for _, g := range globs {
			if globMatch(g, prefix) {
				return true
			}
		}
	}
	return false
}

func globMatch(pattern, name string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(name); i++ {
				if matchSegments(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], name[0])
		if err != nil || !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}
