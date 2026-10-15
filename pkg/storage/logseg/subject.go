package logseg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// SegmentRecorder commits log references under the writer's current authority.
type SegmentRecorder interface {
	RecordSegments(context.Context, store.SegmentCommit) error
}

// Data binds one tagged log to the controller's signer, proxied reader and
// transactional recorder. Credentials stay inside those bindings.
type Data struct {
	Subject    blob.LogSubject
	Transport  blob.LogTransport
	HTTPClient blob.HTTPClient
	Records    SegmentRecorder
	Actor      store.Actor
}

// Read opens a controller-proxied stream at an uncompressed byte offset.
func (d Data) Read(ctx context.Context, offset int64, follow bool) (io.ReadCloser, error) {
	if err := d.Subject.Validate(); err != nil {
		return nil, err
	}
	if d.Transport == nil || offset < 0 {
		return nil, fmt.Errorf("logseg: read requires a transport and nonnegative offset")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body, err := d.Transport.ReadLog(ctx, blob.LogReadRequest{Subject: cloneSubject(d.Subject), Offset: offset, Follow: follow})
	if err != nil && body != nil {
		err = errors.Join(err, body.Close())
		body = nil
	}
	if err == nil && body == nil {
		err = fmt.Errorf("logseg: controller returned no log stream")
	}
	return body, err
}

// SubjectUploader uploads immutable segments and seals their exact ordered list.
// It keeps failed writes retryable, advancing offsets only after record commit.
type SubjectUploader struct {
	data Data
	mu   sync.Mutex

	batch    blob.LogUploadBatch
	prefix   string
	segments []Segment
	bytes    int64
	pending  *Segment
	final    []byte
	sealed   bool
}

var _ Uploader = (*SubjectUploader)(nil)

// NewSubjectUploader validates the binding before any signed transfer is requested.
func NewSubjectUploader(data Data) (*SubjectUploader, error) {
	if err := data.Subject.Validate(); err != nil {
		return nil, err
	}
	if data.Transport == nil || data.Records == nil {
		return nil, fmt.Errorf("logseg: uploader requires a transport and segment recorder")
	}
	if data.HTTPClient == nil {
		data.HTTPClient = blob.TransferClient{}
	}
	data.Subject = cloneSubject(data.Subject)
	return &SubjectUploader{data: data}, nil
}

func cloneSubject(s blob.LogSubject) blob.LogSubject {
	if s.Attempt != nil {
		a := *s.Attempt
		s.Attempt = &a
	}
	if s.Unit != nil {
		u := *s.Unit
		s.Unit = &u
		if u.Source != nil {
			source := *u.Source
			s.Unit.Source = &source
		}
	}
	return s
}

// PutSegment verifies compressed bytes before uploading, then commits their
// uncompressed range with the same subject, generation and retention class.
func (u *SubjectUploader) PutSegment(ctx context.Context, seg Segment, body []byte) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if u.final != nil {
		return ErrSealed
	}
	if seg.N > 0 && seg.N <= len(u.segments) {
		if u.segments[seg.N-1] != seg {
			return blob.ErrIntegrity
		}
		return verifySegment(seg, body)
	}
	if seg.N != len(u.segments)+1 || seg.N > limits.MaxLogSegmentNumber || (u.pending != nil && *u.pending != seg) {
		return blob.ErrIntegrity
	}
	if err := verifySegment(seg, body); err != nil {
		return err
	}
	if seg.FirstSeq != u.lastSeq()+1 || seg.Bytes > limits.StoredAttemptLogBytes-u.bytes {
		return blob.ErrIntegrity
	}
	if err := u.sign(ctx, int64(seg.N)); err != nil {
		return err
	}
	u.pending = &seg
	slot := u.batch.Segments[int64(seg.N)-u.batch.Segments[0].Index]
	if err := u.put(ctx, int64(seg.N), slot.URL, slot.Headers, slot.ExpiresAt, body); err != nil {
		return err
	}
	record := store.Segment{Index: int64(seg.N), Offset: u.bytes, Size: seg.Bytes, Digest: seg.SHA256, ObjectKey: blob.LogSegmentKey(u.prefix, seg.N)}
	if err := u.commit(ctx, []store.Segment{record}, false, ""); err != nil {
		return err
	}
	u.segments = append(u.segments, seg)
	u.bytes += seg.Bytes
	u.pending = nil
	return nil
}

func verifySegment(seg Segment, body []byte) error {
	if seg.N < 1 || seg.Bytes < 1 || seg.StoredBytes != int64(len(body)) || seg.StoredBytes > limits.LogSegmentUploadBytes ||
		seg.Lines < 1 || seg.FirstSeq < 1 || seg.LastSeq < seg.FirstSeq || seg.LastSeq-seg.FirstSeq != seg.Lines-1 {
		return blob.ErrIntegrity
	}
	raw, err := decodeSegment(body, seg)
	if err != nil {
		return errors.Join(blob.ErrIntegrity, err)
	}
	if int64(len(raw)) != seg.Bytes || int64(bytes.Count(raw, []byte{'\n'})) != seg.Lines || len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return blob.ErrIntegrity
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != seg.SHA256 {
		return blob.ErrIntegrity
	}
	return nil
}

func (u *SubjectUploader) lastSeq() int64 {
	if len(u.segments) == 0 {
		return 0
	}
	return u.segments[len(u.segments)-1].LastSeq
}

func (u *SubjectUploader) sign(ctx context.Context, first int64) error {
	now := time.Now()
	if len(u.batch.Segments) > 0 && first >= u.batch.Segments[0].Index && first <= u.batch.Segments[len(u.batch.Segments)-1].Index && u.batch.ExpiresAt.After(now) {
		if u.batch.Segments[first-u.batch.Segments[0].Index].ExpiresAt.After(now) {
			return nil
		}
	}
	count := min(limits.LogUploadBatch, limits.MaxLogSegmentNumber-int(first)+1)
	batch, err := u.data.Transport.SignSegments(ctx, blob.LogSigningRequest{Subject: cloneSubject(u.data.Subject), FirstSegment: first, Count: count})
	if err != nil {
		return err
	}
	now = time.Now()
	prefix, err := blob.LogSubjectPrefix(batch.RetentionClass, batch.Subject)
	want, wantErr := blob.LogSubjectPrefix(batch.RetentionClass, u.data.Subject)
	if err != nil || wantErr != nil || prefix != want || len(batch.Segments) != count || !validLogURL(batch.ManifestURL) ||
		!batch.ExpiresAt.After(now) || (u.prefix != "" && prefix != u.prefix) {
		return blob.ErrIntegrity
	}
	seen := map[string]bool{batch.ManifestURL: true}
	for i, slot := range batch.Segments {
		if slot.Index != first+int64(i) || !validLogURL(slot.URL) || seen[slot.URL] || !slot.ExpiresAt.After(now) || slot.ExpiresAt.After(batch.ExpiresAt) {
			return blob.ErrIntegrity
		}
		seen[slot.URL] = true
	}
	u.batch, u.prefix = batch, prefix
	return nil
}

func validLogURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.Fragment == ""
}

func (u *SubjectUploader) put(ctx context.Context, index int64, url string, headers map[string]string, expiry time.Time, body []byte) error {
	ctx, cancel := context.WithDeadline(ctx, expiry)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	reader := bytes.NewReader(body)
	response, err := u.data.HTTPClient.Transfer(ctx, blob.SignedTransfer{URL: url, Headers: headers, ExpiresAt: expiry, Size: int64(len(body)), Digest: blob.Digest(hex.EncodeToString(sum[:]))}, reader)
	if response != nil {
		err = errors.Join(err, response.Close())
	}
	if err == nil && reader.Len() != 0 {
		err = blob.ErrIntegrity
	}
	if err != nil {
		if ctx.Err() != nil {
			return errors.Join(err, ctx.Err())
		}
		// safety: a failed or create-only PUT can recover only through the controller's
		// authorized byte verification; HTTP status alone never proves a matching object.
		check := blob.LogUploadCheck{Subject: cloneSubject(u.data.Subject), Index: index, Size: int64(len(body)), Digest: blob.Digest(hex.EncodeToString(sum[:]))}
		if verifyErr := u.data.Transport.VerifyUpload(ctx, check); verifyErr != nil {
			return errors.Join(err, verifyErr)
		}
	}
	return ctx.Err()
}

func (u *SubjectUploader) commit(ctx context.Context, segments []store.Segment, final bool, reason string) error {
	return u.data.Records.RecordSegments(ctx, store.SegmentCommit{
		Subject: cloneSubject(u.data.Subject), Actor: u.data.Actor,
		Segments: segments, Final: final, IncompleteReason: reason, RetentionClass: u.batch.RetentionClass,
	})
}

// PutManifest seals only the segment list already committed by this uploader.
// An identical final write may replay; a changed final write is refused.
func (u *SubjectUploader) PutManifest(ctx context.Context, m Manifest) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !slices.Equal(m.Segments, u.segments) || (m.Status != StatusComplete && m.Status != StatusIncomplete && m.Status != StatusCutOff) ||
		(m.Status == StatusComplete && (m.Reason != "" || m.DroppedLines != 0 || len(m.Gaps) != 0 || u.pending != nil)) ||
		(m.Status != StatusComplete && m.Reason == "") || m.DroppedLines < 0 {
		return blob.ErrIntegrity
	}
	for _, gap := range m.Gaps {
		if gap.From < 1 || gap.To < gap.From {
			return blob.ErrIntegrity
		}
	}
	if m.Subject != nil {
		prefix, err := blob.LogSubjectPrefix(ClassStandard, *m.Subject)
		want, wantErr := blob.LogSubjectPrefix(ClassStandard, u.data.Subject)
		if err != nil || wantErr != nil || prefix != want {
			return blob.ErrIntegrity
		}
	}
	subject := cloneSubject(u.data.Subject)
	m.Subject = &subject
	m.Version = ManifestVersion
	m.Bytes, m.Lines, m.FirstSeq, m.LastSeq = u.bytes, u.lastSeq(), 0, u.lastSeq()
	if len(u.segments) > 0 {
		m.FirstSeq = u.segments[0].FirstSeq
	}
	raw, err := m.Marshal()
	if err != nil {
		return err
	}
	if int64(len(raw)) > limits.LogManifestBytes {
		return blob.ErrIntegrity
	}
	if u.final != nil && !bytes.Equal(u.final, raw) {
		return ErrSealed
	}
	if u.sealed {
		return nil
	}
	if err := u.sign(ctx, max(1, int64(len(u.segments)))); err != nil {
		return err
	}
	// safety: retain the first final payload even after an uncertain PUT so retries cannot overwrite it.
	u.final = bytes.Clone(raw)
	if err := u.put(ctx, 0, u.batch.ManifestURL, u.batch.ManifestHeaders, u.batch.ExpiresAt, raw); err != nil {
		return err
	}
	var records []store.Segment
	var offset int64
	for _, seg := range u.segments {
		records = append(records, store.Segment{Index: int64(seg.N), Offset: offset, Size: seg.Bytes, Digest: seg.SHA256, ObjectKey: blob.LogSegmentKey(u.prefix, seg.N)})
		offset += seg.Bytes
	}
	if err := u.commit(ctx, records, true, m.Reason); err != nil {
		return err
	}
	u.sealed = true
	return nil
}
