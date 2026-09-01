package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

type artifactManifest struct {
	Entries []artifactEntry `json:"entries"`
}

type artifactEntry struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Mode   uint32 `json:"mode"`
}

func artifactBlobKey(digest string) string { return "artifacts/blobs/" + digest }

func artifactManifestKey(digest string) string { return "artifacts/manifests/" + digest }

func captureArtifacts(ctx context.Context, store storage.ArtifactStore, workspace string, globs []string) (string, error) {
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
		info, serr := os.Stat(p)
		if serr != nil {
			return fmt.Errorf("artifact %q: %w", rel, serr)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		digest, herr := hashFile(p)
		if herr != nil {
			return fmt.Errorf("artifact %q: %w", rel, herr)
		}
		if err := putArtifactBlob(ctx, store, p, digest); err != nil {
			return fmt.Errorf("artifact %q: %w", rel, err)
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
	manifestBytes, err := json.Marshal(artifactManifest{Entries: entries})
	if err != nil {
		return "", fmt.Errorf("marshal manifest: %w", err)
	}
	sum := sha256.Sum256(manifestBytes)
	digest := hex.EncodeToString(sum[:])
	if err := putBytes(ctx, store, artifactManifestKey(digest), manifestBytes); err != nil {
		return "", fmt.Errorf("store manifest: %w", err)
	}
	return digest, nil
}

func putArtifactBlob(ctx context.Context, store storage.ArtifactStore, p, digest string) error {
	key := artifactBlobKey(digest)
	if ok, err := store.Has(ctx, key); err == nil && ok {
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return store.Put(ctx, key, f)
}

func putBytes(ctx context.Context, store storage.ArtifactStore, key string, b []byte) error {
	if ok, err := store.Has(ctx, key); err == nil && ok {
		return nil
	}
	return store.Put(ctx, key, strings.NewReader(string(b)))
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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
