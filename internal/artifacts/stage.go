// Package artifacts publishes declared outputs and stages committed records.
package artifacts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

// Edge names a committed artifact record and its destination prefix.
type Edge struct {
	Record blob.RecordKey
	Into   string
}

// Stage looks up each record under the caller's authority and writes its verified
// members into workspace. A missing, unauthorized or corrupt record fails.
func Stage(ctx context.Context, data Data, workspace string, edges []Edge) (int, error) {
	if len(edges) == 0 {
		return 0, nil
	}
	if data.Bytes == nil || data.Records == nil {
		return 0, fmt.Errorf("artifact data requires bytes and records")
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return 0, err
	}
	defer func() { _ = root.Close() }()
	staged := 0
	for _, edge := range edges {
		if _, err := stageRel(edge.Into, "."); err != nil {
			return staged, err
		}
		result, err := data.Records.LookupRecord(ctx, blob.RecordLookup{Authority: data.Authority, Key: edge.Record, Budget: data.Budget})
		if err != nil {
			return staged, fmt.Errorf("artifact record: %w", err)
		}
		if !result.Found {
			return staged, fmt.Errorf("artifact record: %w", blob.ErrNotFound)
		}
		if result.Record.Key != edge.Record || result.Record.Key.Kind != blob.KindManifest {
			return staged, blob.ErrDigestNotReadable
		}
		manifest, err := readManifest(ctx, data, result.Record.Digest, edge.Record.Team, result.Members)
		if err != nil {
			return staged, fmt.Errorf("artifact manifest: %w", err)
		}
		members := make(map[string]blob.Member, len(result.Members))
		for _, member := range result.Members {
			members[member.Path] = member
		}
		paths := make([]string, 0, len(manifest.Entries))
		ordered := make([]blob.Member, 0, len(manifest.Entries))
		for _, entry := range manifest.Entries {
			member := members[entry.Path]
			rel, err := stageRel(edge.Into, entry.Path)
			if err != nil {
				return staged, err
			}
			paths = append(paths, rel)
			ordered = append(ordered, member)
			delete(members, entry.Path)
		}
		for i, member := range ordered {
			rel := paths[i]
			if err := stageMember(ctx, data, root, member, rel); err != nil {
				return staged, fmt.Errorf("artifact %q: %w", member.Path, err)
			}
			staged++
		}
	}
	return staged, nil
}

func readManifest(ctx context.Context, data Data, digest blob.Digest, team string, members []blob.Member) (blob.Manifest, error) {
	r, err := data.Bytes.Get(ctx, blob.GetRequest{Authority: data.Authority, Digest: digest})
	if err != nil {
		return blob.Manifest{}, err
	}
	b, err := io.ReadAll(r)
	err = errors.Join(err, r.Close())
	if err != nil {
		return blob.Manifest{}, err
	}
	if !r.Verified() || digestOfBytes(b) != digest {
		return blob.Manifest{}, blob.ErrIntegrity
	}
	return blob.DecodeManifest(b, digest, team, members)
}

func stageMember(ctx context.Context, data Data, root *os.Root, member blob.Member, rel string) (err error) {
	if member.Size < 0 {
		return blob.ErrIntegrity
	}
	r, err := data.Bytes.Get(ctx, blob.GetRequest{Authority: data.Authority, Digest: member.Digest})
	if err != nil {
		return err
	}
	defer func() {
		if r != nil {
			err = errors.Join(err, r.Close())
		}
	}()
	parent := filepath.Dir(rel)
	if parent != "." {
		if err := root.MkdirAll(parent, 0o755); err != nil {
			return err
		}
	}
	tmp, err := temporaryName(parent)
	if err != nil {
		return err
	}
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(tmp) }()
	hash := sha256.New()
	n, copyErr := io.CopyN(io.MultiWriter(f, hash), r, member.Size)
	if copyErr != nil {
		_ = f.Close()
		return copyErr
	}
	var extra [1]byte
	extraN, endErr := r.Read(extra[:])
	closeErr := f.Close()
	if closeErr != nil {
		return closeErr
	}
	if n != member.Size || extraN != 0 || endErr != io.EOF || !r.Verified() || blob.Digest(hex.EncodeToString(hash.Sum(nil))) != member.Digest {
		return blob.ErrIntegrity
	}
	err = r.Close()
	r = nil
	if err != nil {
		return err
	}
	if err := root.Chmod(tmp, os.FileMode(member.Mode).Perm()); err != nil {
		return err
	}
	return replaceVerifiedFile(root, tmp, rel)
}

func replaceVerifiedFile(root *os.Root, source, destination string) error {
	if runtime.GOOS == "windows" {
		info, err := root.Lstat(destination)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// safety: Windows refuses replacing a read-only file; preserve its mode if publication fails.
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o200 == 0 {
			if err := root.Chmod(destination, info.Mode().Perm()|0o200); err != nil {
				return err
			}
			if err := root.Rename(source, destination); err != nil {
				return errors.Join(err, root.Chmod(destination, info.Mode().Perm()))
			}
			return nil
		}
	}
	return root.Rename(source, destination)
}

func digestOfBytes(b []byte) blob.Digest {
	s := sha256.Sum256(b)
	return blob.Digest(hex.EncodeToString(s[:]))
}

func temporaryName(parent string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return filepath.Join(parent, ".sparkwing-artifact-"+hex.EncodeToString(random[:])), nil
}

func stageRel(into, relPath string) (string, error) {
	into = filepath.FromSlash(into)
	relPath = filepath.FromSlash(relPath)
	if strings.HasPrefix(into, string(filepath.Separator)) || strings.HasPrefix(relPath, string(filepath.Separator)) || filepath.VolumeName(into) != "" || filepath.VolumeName(relPath) != "" {
		return "", fmt.Errorf("staged path %q must be relative to the workspace", relPath)
	}
	for _, part := range []string{into, relPath} {
		clean := filepath.Clean(part)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("staged path %q escapes workspace", part)
		}
	}
	rel := filepath.Clean(filepath.Join(into, relPath))
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("staged path %q escapes workspace", rel)
	}
	return rel, nil
}

// CheckPaths refuses output patterns and destination prefixes outside a workspace.
func CheckPaths(globs []string, edges []Edge) error {
	for _, g := range globs {
		if _, err := stageRel(g, "."); err != nil {
			return fmt.Errorf("output %q must stay inside the workspace: %w", g, err)
		}
	}
	for _, e := range edges {
		if _, err := stageRel(e.Into, "."); err != nil {
			return fmt.Errorf("consume into %q: prefix must stay inside the workspace: %w", e.Into, err)
		}
	}
	return nil
}
