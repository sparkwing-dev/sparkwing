package logseg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// UploadVerifier checks an immutable object after an uncertain PUT. The controller
// must authorize the exact writer and resolve its persisted class before calling
// Verify. It must repeat that authorization in the subsequent record transaction.
type UploadVerifier struct{ Objects SegmentObjects }

// Verify derives the key from the subject and server-resolved retention class,
// then consumes and closes the object before confirming its exact size and digest.
// It neither creates a record nor grants download authority.
func (v UploadVerifier) Verify(ctx context.Context, check blob.LogUploadCheck, class string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if v.Objects == nil {
		return fmt.Errorf("logseg: upload verification requires objects")
	}
	prefix, err := SubjectPrefix(class, check.Subject)
	if err != nil {
		return err
	}
	if check.Index < 0 || check.Index > int64(limits.MaxLogSegmentNumber) {
		return blob.ErrIntegrity
	}
	key, limit := ManifestKey(prefix), limits.LogManifestBytes
	if check.Index > 0 {
		key, limit = SegmentKey(prefix, int(check.Index)), limits.LogSegmentUploadBytes
	}
	digest, err := hex.DecodeString(string(check.Digest))
	if check.Size <= 0 || check.Size > limit || err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != string(check.Digest) {
		return blob.ErrIntegrity
	}
	raw, err := readObject(ctx, v.Objects, key, check.Size)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if int64(len(raw)) != check.Size || hex.EncodeToString(sum[:]) != string(check.Digest) {
		return blob.ErrIntegrity
	}
	return nil
}
