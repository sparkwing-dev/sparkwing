package artifacts

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

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

var (
	errArtifactOutsideWorkspace = errors.New("resolves outside the workspace")
	errArtifactUnresolvable     = errors.New("cannot resolve")
)

// Data binds artifact byte and record operations to the same caller authority.
type Data struct {
	Bytes     blob.Store
	Records   blob.RecordStore
	Authority blob.Authority
	Identity  blob.UploadIdentity
	Pipeline  string
	Budget    blob.Budget
}

// Capture uploads matched workspace files, commits their authorized manifest,
// and returns the committed record. A wildcard match outside the workspace is
// skipped with a warning; a literal outside match fails.
func Capture(ctx context.Context, data Data, workspace string, globs []string, warn func(msg string)) (blob.Record, error) {
	if err := ctx.Err(); err != nil {
		return blob.Record{}, err
	}
	if data.Bytes == nil || data.Records == nil || warn == nil {
		return blob.Record{}, fmt.Errorf("artifact data requires bytes, records and warning sink")
	}
	if err := blob.AuthorizeUpload(data.Authority, data.Identity, blob.KindManifest); err != nil {
		return blob.Record{}, err
	}
	job := ""
	if data.Identity.Attempt.Set {
		job = data.Identity.Attempt.Value.NodeID
	}
	key, err := blob.ResolveWriteKey(data.Authority, blob.WriteScopeIntent{Kind: blob.KindManifest, Pipeline: data.Pipeline, Job: job, Name: data.Identity.RunID})
	if err != nil {
		return blob.Record{}, fmt.Errorf("artifact record key: %w", err)
	}
	wsRoot, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return blob.Record{}, fmt.Errorf("resolve workspace: %w", err)
	}
	root, err := os.OpenRoot(wsRoot)
	if err != nil {
		return blob.Record{}, fmt.Errorf("open workspace: %w", err)
	}
	defer func() { _ = root.Close() }()

	var entries []blob.ManifestEntry
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
				warn(fmt.Sprintf("artifact %q resolves outside the workspace; skipping", rel))
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
		digest, size, berr := storeArtifactFile(ctx, data, root, name)
		if berr != nil {
			return fmt.Errorf("artifact %q: %w", rel, berr)
		}
		entries = append(entries, blob.ManifestEntry{
			Path:   rel,
			Digest: digest,
			Size:   size,
			Mode:   uint32(info.Mode().Perm()),
		})
		return nil
	})
	if walkErr != nil {
		return blob.Record{}, walkErr
	}
	if err := ctx.Err(); err != nil {
		return blob.Record{}, err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	manifestBytes, merr := json.Marshal(blob.Manifest{Entries: entries})
	if merr != nil {
		return blob.Record{}, fmt.Errorf("marshal manifest: %w", merr)
	}
	sum := sha256.Sum256(manifestBytes)
	digest := blob.Digest(hex.EncodeToString(sum[:]))
	receipt, err := data.Bytes.Put(ctx, blob.PutRequest{Identity: data.Identity, Authority: data.Authority, Digest: digest, Size: int64(len(manifestBytes)), Body: bytes.NewReader(manifestBytes)})
	if err = errors.Join(err, ctx.Err()); err != nil {
		return blob.Record{}, fmt.Errorf("store manifest: %w", err)
	}
	if receipt.Digest != digest || receipt.Identity != data.Identity {
		return blob.Record{}, blob.ErrIntegrity
	}
	members := make([]blob.Member, 0, len(entries))
	for _, entry := range entries {
		members = append(members, blob.Member{Team: key.Team, ManifestDigest: digest, Digest: entry.Digest, Path: entry.Path, Size: entry.Size, Mode: entry.Mode})
	}
	record, err := data.Records.CommitRecord(ctx, blob.CommitRecordRequest{Source: data.Identity, Authority: data.Authority, Key: key, Digest: digest, Members: members})
	if err = errors.Join(err, ctx.Err()); err != nil {
		return blob.Record{}, fmt.Errorf("commit artifact manifest: %w", err)
	}
	if record.Key != key || record.Digest != digest || record.Source != data.Identity {
		return blob.Record{}, blob.ErrIntegrity
	}
	return record, nil
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
func storeArtifactFile(ctx context.Context, data Data, root *os.Root, name string) (blob.Digest, int64, error) {
	f, err := root.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	digest := blob.Digest(hex.EncodeToString(h.Sum(nil)))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	receipt, err := data.Bytes.Put(ctx, blob.PutRequest{Identity: data.Identity, Authority: data.Authority, Digest: digest, Size: n, Body: f})
	if err = errors.Join(err, ctx.Err()); err != nil {
		return "", 0, err
	}
	if receipt.Digest != digest || receipt.Identity != data.Identity {
		return "", 0, blob.ErrIntegrity
	}
	return digest, n, nil
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
