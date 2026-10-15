package nodehost

import (
	"bytes"
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

	"github.com/sparkwing-dev/sparkwing/internal/artifacts"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

type artifactFile struct {
	Path   string      `json:"path"`
	Digest blob.Digest `json:"digest"`
	Size   int64       `json:"size"`
	Mode   uint32      `json:"mode"`
}

// Publish uploads matching workspace files and commits their manifest under the
// current job attempt. Bind supplies its authenticated destination. The caller
// attaches the returned key through the fenced terminal result transaction.
// Wildcard matches outside the workspace are skipped with a warning; a literal
// outside file is an error. No successful key escapes a failed upload or commit.
func (a ArtifactRecords) Publish(ctx context.Context, input NodeInput, workspace string, patterns []string, warn func(string)) (*blob.RecordKey, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if input.Storage.Bytes != nil || input.Storage.Records != nil {
		if input.Storage.Bytes == nil || input.Storage.Records == nil {
			return nil, errors.New("artifact storage binding is incomplete")
		}
		a.Blobs, a.Records = input.Storage.Bytes, input.Storage.Records
	}
	if a.Blobs == nil || a.Records == nil || a.Bind == nil || workspace == "" {
		return nil, errors.New("artifact publication requires bytes, records, workspace and authenticated binding")
	}
	if err := checkArtifactPatterns(patterns); err != nil {
		return nil, err
	}
	upload, err := a.Bind(ctx, input)
	if err != nil {
		return nil, err
	}
	if input.Storage.Authority.CapabilityRef != "" && input.Storage.Authority != upload.Authority {
		return nil, errors.New("artifact authority differs from the current storage binding")
	}
	if err := upload.check(input, blob.KindManifest); err != nil {
		return nil, err
	}
	files, err := a.capture(ctx, upload, workspace, patterns, warn)
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	value, err := json.Marshal(struct {
		Files []artifactFile `json:"files"`
	}{files})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(value)
	digest := blob.Digest(hex.EncodeToString(sum[:]))
	if err := upload.put(ctx, a.Blobs, digest, int64(len(value)), bytes.NewReader(value)); err != nil {
		return nil, err
	}
	members := make([]blob.Member, 0, len(files))
	for _, file := range files {
		members = append(members, blob.Member{Team: upload.Key.Team, ManifestDigest: digest, Digest: file.Digest, Path: file.Path, Size: file.Size, Mode: file.Mode})
	}
	return upload.commit(ctx, a.Records, digest, members)
}

func checkArtifactPatterns(patterns []string) error {
	for _, pattern := range patterns {
		if !filepath.IsLocal(pattern) || !fs.ValidPath(pattern) || strings.Contains(pattern, "\\") {
			return errors.New("artifact patterns must stay inside the workspace")
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return errors.New("artifact glob is malformed")
		}
	}
	return nil
}

func (a ArtifactRecords) capture(ctx context.Context, upload JobUpload, workspace string, patterns []string, warn func(string)) (files []artifactFile, err error) {
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, errors.New("artifact workspace cannot be resolved")
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	files = []artifactFile{}
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if name == "." || entry.IsDir() || !artifacts.Matches(patterns, name) {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			skip, err := outsideArtifact(workspace, name, patterns, warn)
			if err != nil || skip {
				return err
			}
		}
		file, err := a.captureFile(ctx, upload, root, name)
		if err != nil {
			return fmt.Errorf("capture artifact %q: %w", name, err)
		}
		if file != nil {
			files = append(files, *file)
		}
		return nil
	})
	return files, err
}

func outsideArtifact(workspace, name string, patterns []string, warn func(string)) (bool, error) {
	resolved, err := filepath.EvalSymlinks(filepath.Join(workspace, filepath.FromSlash(name)))
	if err != nil {
		return false, errors.New("artifact link cannot be resolved")
	}
	rel, err := filepath.Rel(workspace, resolved)
	if err != nil {
		return false, errors.New("artifact link cannot be resolved")
	}
	if filepath.IsLocal(rel) {
		return false, nil
	}
	literal := false
	for _, pattern := range patterns {
		literal = literal || pattern == name && !strings.ContainsAny(pattern, "*?[")
	}
	if literal {
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			return false, errors.New("artifact resolves outside the workspace")
		}
	}
	if warn != nil {
		warn(fmt.Sprintf("artifact %q resolves outside the workspace; skipping", name))
	}
	return true, nil
}

func (a ArtifactRecords) captureFile(ctx context.Context, upload JobUpload, root *os.Root, name string) (entry *artifactFile, err error) {
	info, err := root.Stat(filepath.FromSlash(name))
	if err != nil {
		return nil, errors.New("artifact cannot be read inside the workspace")
	}
	if !info.Mode().IsRegular() {
		return nil, nil
	}
	// safety: the rooted open enforces confinement even if symlinks change after inspection.
	file, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return nil, errors.New("artifact cannot be opened inside the workspace")
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("artifact changed file type during capture")
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return nil, err
	}
	digest := blob.Digest(hex.EncodeToString(hash.Sum(nil)))
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	// safety: Put verifies the signed size and hash again; a changing file cannot commit stale bytes.
	if err := upload.put(ctx, a.Blobs, digest, size, file); err != nil {
		return nil, err
	}
	return &artifactFile{Path: name, Digest: digest, Size: size, Mode: uint32(info.Mode().Perm())}, nil
}
