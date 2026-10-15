package nodehost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

// ArtifactRecords publishes files and stages authorized manifest members.
// The caller resolves the producer's persisted record key under the consumer's
// live authority. A key or digest alone never grants a read.
type ArtifactRecords struct {
	Blobs   blob.Store
	Records blob.RecordStore
	Bind    func(context.Context, NodeInput) (JobUpload, error)
	// Producer resolves a declared producer's persisted terminal manifest reference.
	Producer func(context.Context, NodeInput, string) (blob.RecordKey, error)
	Warn     func(string)
}

// Stage restores required artifacts below workspace/into. It verifies the
// manifest and every file through EOF and Close before replacing that file.
// Failure leaves the previous destination intact and removes partial downloads.
// The returned count includes only successfully installed files.
func (a ArtifactRecords) Stage(ctx context.Context, request blob.RecordLookup, workspace, into string) (count int, err error) {
	if err := context.Cause(ctx); err != nil {
		return 0, err
	}
	if a.Blobs == nil || a.Records == nil || request.Key.Kind != blob.KindManifest || request.Key.Team == "" || workspace == "" {
		return 0, errors.New("artifact staging requires bytes, records, workspace and a manifest key")
	}
	if into == "" {
		into = "."
	}
	if !filepath.IsLocal(into) {
		return 0, errors.New("artifact destination must stay inside the workspace")
	}
	lookup, err := a.Records.LookupRecord(ctx, request)
	if err != nil {
		return 0, err
	}
	if !lookup.Found {
		return 0, blob.ErrNotFound
	}
	if lookup.Record.Key != request.Key || !artifactDigestValid(lookup.Record.Digest) {
		return 0, errors.New("artifact lookup changed its requested record")
	}
	if err := checkArtifactMembers(lookup); err != nil {
		return 0, err
	}
	// safety: membership comes from the authorized record transaction, never an
	// untrusted manifest body. The manifest's bytes must still exist and verify.
	if _, err := a.copyVerified(ctx, request.Authority, lookup.Record.Digest, io.Discard, -1); err != nil {
		return 0, fmt.Errorf("read required artifact manifest: %w", err)
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	for _, member := range lookup.Members {
		name := filepath.Join(into, filepath.FromSlash(member.Path))
		if err := a.stageMember(ctx, root, request.Authority, member, name); err != nil {
			return count, fmt.Errorf("stage artifact %q: %w", member.Path, err)
		}
		count++
	}
	return count, nil
}

func checkArtifactMembers(lookup blob.RecordLookupResult) error {
	paths := make(map[string]bool, len(lookup.Members))
	for _, member := range lookup.Members {
		if member.Team != lookup.Record.Key.Team || member.ManifestDigest != lookup.Record.Digest || !artifactDigestValid(member.Digest) || member.Size < 0 {
			return errors.New("artifact member differs from its manifest or has invalid metadata")
		}
		if member.Path == "." || !fs.ValidPath(member.Path) || !filepath.IsLocal(filepath.FromSlash(member.Path)) || paths[member.Path] {
			return errors.New("artifact member path is invalid or duplicated")
		}
		paths[member.Path] = true
	}
	for name := range paths {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if paths[parent] {
				return errors.New("artifact member is also a parent directory")
			}
		}
	}
	return nil
}

func artifactDigestValid(digest blob.Digest) bool {
	if len(digest) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(string(digest))
	return err == nil && hex.EncodeToString(decoded) == string(digest)
}

func (a ArtifactRecords) copyVerified(ctx context.Context, authority blob.Authority, digest blob.Digest, dst io.Writer, expected int64) (size int64, err error) {
	if err := context.Cause(ctx); err != nil {
		return 0, err
	}
	reader, err := a.Blobs.Get(ctx, blob.GetRequest{Authority: authority, Digest: digest})
	if reader != nil {
		defer func() { err = errors.Join(err, reader.Close()) }()
	}
	if err != nil {
		return 0, err
	}
	if reader == nil {
		return 0, errors.New("artifact backend returned no reader")
	}
	// safety: an embedded WriterTo can bypass Read's EOF verification.
	var src io.Reader = struct{ io.Reader }{reader}
	if expected >= 0 && expected < math.MaxInt64 {
		src = io.LimitReader(src, expected+1)
	}
	size, err = io.Copy(dst, src)
	if err == nil && !reader.Verified() {
		err = blob.ErrIntegrity
	}
	return size, errors.Join(err, context.Cause(ctx))
}

func (a ArtifactRecords) stageMember(ctx context.Context, root *os.Root, authority blob.Authority, member blob.Member, name string) (err error) {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	parent := filepath.Dir(name)
	if err := root.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	// safety: a sibling temporary file preserves the old destination on failed
	// verification, and rename replaces a final symlink without following it.
	temporary := filepath.Join(parent, ".sparkwing-artifact-"+rand.Text())
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	installed, closed := false, false
	defer func() {
		if !closed {
			err = errors.Join(err, file.Close())
		}
		if !installed {
			err = errors.Join(err, root.Remove(temporary))
		}
	}()
	size, err := a.copyVerified(ctx, authority, member.Digest, file, member.Size)
	if err != nil {
		return err
	}
	if size != member.Size {
		return blob.ErrIntegrity
	}
	if err := file.Chmod(os.FileMode(member.Mode).Perm()); err != nil {
		return err
	}
	err = file.Close()
	closed = true
	if err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := root.Rename(temporary, name); err != nil {
		return err
	}
	installed = true
	return nil
}
