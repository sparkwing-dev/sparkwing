// Package teamblob keeps the cache and logs services' bytes in an
// S3-compatible bucket, one namespace per team.
//
// Every object a team writes lives under
//
//	<prefix>/teams/<team>/<rel>
//
// which is the layout the cache already uses on disk, so a team's whole
// footprint is one prefix: deleting it is one listing walk, and a key
// built for one team can never name another's object. The operator's own
// objects, written without a team, live at <prefix>/<rel>; a rel whose
// first segment is "teams" or "_meta" is refused so the operator
// namespace cannot reach into either.
//
// The store counts nothing itself: the controller counts what each team
// stores, and [Store.Measure] is the listing its storage pass reconciles
// that count from.
//
// Requests go through the client the caller hands in. Built by
// storeurl.OpenS3, that client caps the SDK's own retries and spends a
// process-wide request budget on each attempt, so nothing here retries
// on its own: a failing bucket costs at most the SDK's capped attempts
// per call, never a loop.
package teamblob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// Client is the part of the S3 API the store uses. *s3.Client
// satisfies it; tests substitute a counting wrapper.
type Client interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, opt ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, opt ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, opt ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, opt ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, opt ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opt ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	CreateMultipartUpload(ctx context.Context, in *s3.CreateMultipartUploadInput, opt ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error)
	UploadPart(ctx context.Context, in *s3.UploadPartInput, opt ...func(*s3.Options)) (*s3.UploadPartOutput, error)
	CompleteMultipartUpload(ctx context.Context, in *s3.CompleteMultipartUploadInput, opt ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error)
	AbortMultipartUpload(ctx context.Context, in *s3.AbortMultipartUploadInput, opt ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error)
}

// ErrNotFound reports an object that does not exist.
var ErrNotFound = errors.New("teamblob: not found")

// ErrInvalidKey reports a team or relative key the store refuses to
// build an object key from.
var ErrInvalidKey = errors.New("teamblob: invalid key")

const (
	// DefaultPartSize is one multipart part. Sixteen MiB keeps a 500 MiB
	// artifact to 32 part requests while holding one part in memory.
	DefaultPartSize int64 = 16 << 20
	// DefaultMultipartThreshold is the body size above which a write
	// goes multipart. A single PUT can carry 5 GiB, but a long single
	// PUT that fails resends everything, and a part that fails does not
	// leave a half object: the upload is aborted.
	DefaultMultipartThreshold int64 = 64 << 20
	// DefaultMaxListPages bounds one listing walk. A thousand pages is a
	// million keys, far past one team's allowance.
	DefaultMaxListPages = 1000

	teamsSegment = "teams"
	metaSegment  = "_meta"
	deleteBatch  = 1000
	abortTimeout = 30 * time.Second
)

// Options configures a [Store].
type Options struct {
	Bucket string
	// Prefix is the store's root inside the bucket; empty is the bucket
	// root. Two services sharing a bucket take different prefixes.
	Prefix string
	Client Client
	// PartSize and MultipartThreshold default to DefaultPartSize and
	// DefaultMultipartThreshold. S3 refuses parts under 5 MiB other than
	// the last, so a smaller PartSize only suits a test fake.
	PartSize           int64
	MultipartThreshold int64
	// MaxListPages defaults to DefaultMaxListPages.
	MaxListPages int
	// TeamObjectMaxAge, when set, makes [Store.Measure] delete every
	// object of a team last written longer ago than the age it answers for
	// that team, in the listing it already makes. Zero keeps the team's
	// objects; the operator's own namespace is never expired.
	TeamObjectMaxAge func(team string) time.Duration
	// Now defaults to time.Now.
	Now func() time.Time
}

// Store is a team-namespaced object store over one bucket prefix.
type Store struct {
	bucket    string
	prefix    string
	client    Client
	partSize  int64
	threshold int64
	maxPages  int
	maxAge    func(team string) time.Duration
	now       func() time.Time
	breaker   breaker
}

// New validates opts and returns a Store.
func New(opts Options) (*Store, error) {
	if opts.Bucket == "" {
		return nil, errors.New("teamblob: a bucket is required")
	}
	if opts.Client == nil {
		return nil, errors.New("teamblob: a client is required")
	}
	// safety: a store at the bucket root would list and write the whole
	// bucket, which the per-service IAM policies deny and every other
	// service's data sits in.
	if strings.Trim(opts.Prefix, "/") == "" {
		return nil, errors.New("teamblob: a prefix is required; name the service's own prefix, as in s3://bucket/logs")
	}
	s := &Store{
		bucket:    opts.Bucket,
		prefix:    strings.Trim(opts.Prefix, "/"),
		client:    opts.Client,
		partSize:  opts.PartSize,
		threshold: opts.MultipartThreshold,
		maxPages:  opts.MaxListPages,
		maxAge:    opts.TeamObjectMaxAge,
		now:       opts.Now,
	}
	if s.partSize <= 0 {
		s.partSize = DefaultPartSize
	}
	if s.threshold <= 0 {
		s.threshold = DefaultMultipartThreshold
	}
	if s.threshold < s.partSize {
		s.threshold = s.partSize
	}
	if s.maxPages <= 0 {
		s.maxPages = DefaultMaxListPages
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s, nil
}

// Bucket names the bucket the store writes to.
func (s *Store) Bucket() string { return s.bucket }

// Prefix is the store's root inside the bucket.
func (s *Store) Prefix() string { return s.prefix }

// Object describes one stored object. Rel is relative to the team's
// namespace, the same spelling the caller wrote it under.
type Object struct {
	Rel          string
	Size         int64
	LastModified time.Time
	Metadata     map[string]string
}

// ValidTeam reports whether team is a slug the store will namespace: a
// DNS label, which is what a team name is.
func ValidTeam(team string) bool {
	if team == "" || len(team) > 63 || team[0] == '-' || team[len(team)-1] == '-' {
		return false
	}
	for _, c := range team {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// validRel holds a relative key to segments an object store and every
// reader treat the same way: no empty, "." or ".." segment, no
// backslash and no control character.
func validRel(rel string, allowTrailingSlash bool) bool {
	if rel == "" || len(rel) > 900 || strings.HasPrefix(rel, "/") {
		return false
	}
	body := rel
	if allowTrailingSlash {
		body = strings.TrimSuffix(rel, "/")
		if body == "" {
			return false
		}
	}
	for seg := range strings.SplitSeq(body, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	for _, r := range rel {
		if r == '\\' || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func (s *Store) root() string {
	if s.prefix == "" {
		return ""
	}
	return s.prefix + "/"
}

// namespace returns the key prefix every object of team sits under. The
// operator's namespace, team "", is the store root.
func (s *Store) namespace(team string) (string, error) {
	if team == "" {
		return s.root(), nil
	}
	if !ValidTeam(team) {
		return "", fmt.Errorf("%w: team %q is not a slug", ErrInvalidKey, team)
	}
	return s.root() + teamsSegment + "/" + team + "/", nil
}

// safety: the operator namespace is the store root, which holds every
// team's namespace and the store's own metadata, so an operator key may
// not start with either reserved segment.
func reservedOperatorRel(rel string) bool {
	first, _, _ := strings.Cut(rel, "/")
	return first == teamsSegment || first == metaSegment
}

// Key returns the full object key for team's rel.
func (s *Store) Key(team, rel string) (string, error) {
	return s.key(team, rel, false)
}

func (s *Store) key(team, rel string, prefix bool) (string, error) {
	if !validRel(rel, prefix) {
		return "", fmt.Errorf("%w: %q", ErrInvalidKey, rel)
	}
	if team == "" && reservedOperatorRel(rel) {
		return "", fmt.Errorf("%w: %q is reserved", ErrInvalidKey, rel)
	}
	ns, err := s.namespace(team)
	if err != nil {
		return "", err
	}
	return ns + rel, nil
}

// PutOptions describes one write.
type PutOptions struct {
	// Size is the body length when known, or -1. A known size at or
	// under the multipart threshold goes out as one PUT without
	// buffering.
	Size        int64
	ContentType string
	Metadata    map[string]string
	// Fresh skips the HEAD that learns what an overwrite replaces. A
	// caller that knows the key is new saves the request; the write then
	// reports itself as a new object.
	Fresh bool
}

// Written is what one Put stored and what it added to the team's
// namespace: an overwrite adds the size difference and no object.
type Written struct {
	Bytes        int64
	AddedBytes   int64
	AddedObjects int64
}

// Put stores body under team's rel. A body larger than the multipart
// threshold is uploaded in parts, and a failed part aborts the upload so
// no partial object or orphaned part remains.
func (s *Store) Put(ctx context.Context, team, rel string, body io.Reader, opts PutOptions) (Written, error) {
	key, err := s.Key(team, rel)
	if err != nil {
		return Written{}, err
	}
	if err := s.breaker.paused(s.now()); err != nil {
		return Written{}, err
	}
	var prior int64 = -1
	if !opts.Fresh {
		if h, err := s.headKey(ctx, key); err == nil {
			prior = h.Size
		} else if !errors.Is(err, ErrNotFound) {
			return Written{}, err
		}
	}
	var n int64
	if opts.Size >= 0 && opts.Size <= s.threshold {
		n, err = s.putSingle(ctx, key, body, opts.Size, opts)
	} else {
		n, err = s.putStreaming(ctx, key, body, opts)
	}
	if err != nil {
		return Written{}, err
	}
	w := Written{Bytes: n, AddedBytes: n, AddedObjects: 1}
	if prior >= 0 {
		w.AddedBytes, w.AddedObjects = n-prior, 0
	}
	return w, nil
}

func (s *Store) putSingle(ctx context.Context, key string, body io.Reader, size int64, opts PutOptions) (int64, error) {
	// perf: the SDK needs a seekable body to sign and to resend within its
	// capped retries, so a known small body is read once into memory.
	data, err := io.ReadAll(io.LimitReader(body, size+1))
	if err != nil {
		return 0, err
	}
	if int64(len(data)) != size {
		return 0, fmt.Errorf("teamblob: body was %d bytes, not the %d declared", len(data), size)
	}
	return s.putBytes(ctx, key, data, opts)
}

func (s *Store) putBytes(ctx context.Context, key string, data []byte, opts PutOptions) (int64, error) {
	in := &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
		Metadata:      opts.Metadata,
	}
	if opts.ContentType != "" {
		in.ContentType = aws.String(opts.ContentType)
	}
	if err := s.guarded(ctx, func() error {
		_, err := s.client.PutObject(ctx, in)
		return err
	}); err != nil {
		return 0, fmt.Errorf("teamblob: put %s: %w", key, err)
	}
	return int64(len(data)), nil
}

// putStreaming reads the first part; a body that ends inside it is one
// PUT, and anything longer is a multipart upload.
func (s *Store) putStreaming(ctx context.Context, key string, body io.Reader, opts PutOptions) (int64, error) {
	first := make([]byte, s.partSize)
	n, err := io.ReadFull(body, first)
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return s.putBytes(ctx, key, first[:n], opts)
	case err != nil:
		return 0, err
	}
	return s.putMultipart(ctx, key, first, body, opts)
}

func (s *Store) putMultipart(ctx context.Context, key string, first []byte, body io.Reader, opts PutOptions) (total int64, err error) {
	in := &s3.CreateMultipartUploadInput{
		Bucket:   aws.String(s.bucket),
		Key:      aws.String(key),
		Metadata: opts.Metadata,
	}
	if opts.ContentType != "" {
		in.ContentType = aws.String(opts.ContentType)
	}
	var created *s3.CreateMultipartUploadOutput
	err = s.guarded(ctx, func() error {
		var cerr error
		created, cerr = s.client.CreateMultipartUpload(ctx, in)
		return cerr
	})
	if err != nil {
		return 0, fmt.Errorf("teamblob: start multipart %s: %w", key, err)
	}
	uploadID := created.UploadId
	defer func() {
		if err == nil {
			return
		}
		// safety: an aborted upload frees its parts, which S3 otherwise
		// bills until a lifecycle rule removes them. The abort gets its
		// own deadline because the caller's may be what failed.
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), abortTimeout)
		defer cancel()
		if _, aerr := s.client.AbortMultipartUpload(actx, &s3.AbortMultipartUploadInput{
			Bucket:   aws.String(s.bucket),
			Key:      aws.String(key),
			UploadId: uploadID,
		}); aerr != nil {
			err = errors.Join(err, fmt.Errorf("abort multipart %s: %w", key, aerr))
		}
	}()

	var parts []types.CompletedPart
	buf := first
	for number := int32(1); len(buf) > 0; number++ {
		var out *s3.UploadPartOutput
		perr := s.guarded(ctx, func() error {
			var uerr error
			out, uerr = s.client.UploadPart(ctx, &s3.UploadPartInput{
				Bucket:        aws.String(s.bucket),
				Key:           aws.String(key),
				UploadId:      uploadID,
				PartNumber:    aws.Int32(number),
				Body:          bytes.NewReader(buf),
				ContentLength: aws.Int64(int64(len(buf))),
			})
			return uerr
		})
		if perr != nil {
			return 0, fmt.Errorf("teamblob: upload part %d of %s: %w", number, key, perr)
		}
		parts = append(parts, types.CompletedPart{ETag: out.ETag, PartNumber: aws.Int32(number)})
		total += int64(len(buf))

		buf = buf[:cap(buf)]
		n, rerr := io.ReadFull(body, buf)
		if rerr != nil && !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrUnexpectedEOF) {
			return 0, rerr
		}
		buf = buf[:n]
	}
	if err := s.guarded(ctx, func() error {
		_, cerr := s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
			Bucket:          aws.String(s.bucket),
			Key:             aws.String(key),
			UploadId:        uploadID,
			MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
		})
		return cerr
	}); err != nil {
		return 0, fmt.Errorf("teamblob: complete multipart %s: %w", key, err)
	}
	return total, nil
}

// Get opens team's rel. The caller closes the reader.
func (s *Store) Get(ctx context.Context, team, rel string) (io.ReadCloser, Object, error) {
	key, err := s.Key(team, rel)
	if err != nil {
		return nil, Object{}, err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if isNotFound(err) {
			return nil, Object{}, ErrNotFound
		}
		return nil, Object{}, fmt.Errorf("teamblob: get %s: %w", key, err)
	}
	return out.Body, Object{
		Rel:          rel,
		Size:         aws.ToInt64(out.ContentLength),
		LastModified: aws.ToTime(out.LastModified),
		Metadata:     out.Metadata,
	}, nil
}

// ReadAll returns team's rel whole, for small objects such as indexes.
func (s *Store) ReadAll(ctx context.Context, team, rel string) ([]byte, error) {
	rc, _, err := s.Get(ctx, team, rel)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// Head describes team's rel without reading it.
func (s *Store) Head(ctx context.Context, team, rel string) (Object, error) {
	key, err := s.Key(team, rel)
	if err != nil {
		return Object{}, err
	}
	o, err := s.headKey(ctx, key)
	o.Rel = rel
	return o, err
}

func (s *Store) headKey(ctx context.Context, key string) (Object, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if isNotFound(err) {
			return Object{}, ErrNotFound
		}
		return Object{}, fmt.Errorf("teamblob: head %s: %w", key, err)
	}
	return Object{
		Size:         aws.ToInt64(out.ContentLength),
		LastModified: aws.ToTime(out.LastModified),
		Metadata:     out.Metadata,
	}, nil
}

// Delete removes team's rel. A missing object is not an error.
func (s *Store) Delete(ctx context.Context, team, rel string) error {
	key, err := s.Key(team, rel)
	if err != nil {
		return err
	}
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}); err != nil && !isNotFound(err) {
		return fmt.Errorf("teamblob: delete %s: %w", key, err)
	}
	return nil
}

// Sized names an object and the size the caller knows it has.
type Sized struct {
	Rel  string
	Size int64
}

// DeleteMany removes the named objects of team in batches of a thousand
// per request.
func (s *Store) DeleteMany(ctx context.Context, team string, objs []Sized) error {
	keys := make([]sizedKey, 0, len(objs))
	for _, o := range objs {
		k, err := s.Key(team, o.Rel)
		if err != nil {
			return err
		}
		keys = append(keys, sizedKey{key: k, size: o.Size})
	}
	_, err := s.deleteKeys(ctx, keys)
	return err
}

type sizedKey struct {
	key  string
	size int64
}

// deleteKeys reports what the store confirmed deleted: every key of a
// batch it answered, less the keys it listed as failed. A batch that
// failed outright deleted nothing the caller can count.
func (s *Store) deleteKeys(ctx context.Context, keys []sizedKey) (Deleted, error) {
	var d Deleted
	var errs []error
	for start := 0; start < len(keys); start += deleteBatch {
		batch := keys[start:min(start+deleteBatch, len(keys))]
		ids := make([]types.ObjectIdentifier, 0, len(batch))
		for _, k := range batch {
			ids = append(ids, types.ObjectIdentifier{Key: aws.String(k.key)})
		}
		out, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.bucket),
			Delete: &types.Delete{Objects: ids, Quiet: aws.Bool(true)},
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("teamblob: delete %d objects: %w", len(ids), err))
			break
		}
		failed := map[string]bool{}
		for _, e := range out.Errors {
			failed[aws.ToString(e.Key)] = true
		}
		for _, k := range batch {
			if !failed[k.key] {
				d.Objects++
				d.Bytes += k.size
			}
		}
		if len(out.Errors) > 0 {
			e := out.Errors[0]
			errs = append(errs, fmt.Errorf("teamblob: delete: %d object(s) failed; %s: %s", len(out.Errors), aws.ToString(e.Key), aws.ToString(e.Code)))
		}
	}
	return d, errors.Join(errs...)
}

// ErrListTruncated reports a listing that stopped at its page cap.
var ErrListTruncated = errors.New("teamblob: listing stopped at its page cap")

// List returns every object of team under relPrefix, which may end in a
// slash. It costs one LIST request per thousand objects and stops with
// ErrListTruncated at the store's page cap, so a caller uses it for one
// run or one job, never on a timer across a team.
func (s *Store) List(ctx context.Context, team, relPrefix string) ([]Object, error) {
	full, strip, err := s.listPrefix(team, relPrefix)
	if err != nil {
		return nil, err
	}
	var out []Object
	err = s.walk(ctx, full, func(o types.Object) {
		out = append(out, Object{
			Rel:          strings.TrimPrefix(aws.ToString(o.Key), strip),
			Size:         aws.ToInt64(o.Size),
			LastModified: aws.ToTime(o.LastModified),
		})
	})
	return out, err
}

func (s *Store) listPrefix(team, relPrefix string) (full, strip string, err error) {
	ns, err := s.namespace(team)
	if err != nil {
		return "", "", err
	}
	if relPrefix == "" {
		if team == "" {
			return "", "", fmt.Errorf("%w: the operator namespace is listed by rel prefix, never whole", ErrInvalidKey)
		}
		return ns, ns, nil
	}
	full, err = s.key(team, relPrefix, true)
	return full, ns, err
}

func (s *Store) walk(ctx context.Context, prefix string, visit func(types.Object)) error {
	var token *string
	for page := 0; ; page++ {
		if page >= s.maxPages {
			return ErrListTruncated
		}
		var out *s3.ListObjectsV2Output
		err := s.guarded(ctx, func() error {
			var lerr error
			out, lerr = s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
				Bucket:            aws.String(s.bucket),
				Prefix:            aws.String(prefix),
				ContinuationToken: token,
			})
			return lerr
		})
		if err != nil {
			return fmt.Errorf("teamblob: list %s: %w", prefix, err)
		}
		for _, o := range out.Contents {
			visit(o)
		}
		if !aws.ToBool(out.IsTruncated) {
			return nil
		}
		token = out.NextContinuationToken
	}
}

// Deleted is what a prefix delete removed.
type Deleted struct {
	Objects int64 `json:"objects"`
	Bytes   int64 `json:"bytes"`
}

// DeletePrefix removes every object of team under relPrefix: one LIST
// per thousand objects and one DeleteObjects per thousand. An empty
// relPrefix on a team deletes the team's whole namespace. The result
// carries only deletions the store confirmed.
func (s *Store) DeletePrefix(ctx context.Context, team, relPrefix string) (Deleted, error) {
	full, _, err := s.listPrefix(team, relPrefix)
	if err != nil {
		return Deleted{}, err
	}
	var d Deleted
	var keys []sizedKey
	var derr error
	flush := func() {
		if len(keys) == 0 || derr != nil {
			return
		}
		got, err := s.deleteKeys(ctx, keys)
		d.Objects += got.Objects
		d.Bytes += got.Bytes
		derr = err
		keys = keys[:0]
	}
	walkErr := s.walk(ctx, full, func(o types.Object) {
		if derr != nil {
			return
		}
		keys = append(keys, sizedKey{key: aws.ToString(o.Key), size: aws.ToInt64(o.Size)})
		if len(keys) == deleteBatch {
			flush()
		}
	})
	if walkErr == nil {
		flush()
	}
	return d, errors.Join(walkErr, derr)
}

// DeleteTeam removes team's whole namespace. It is idempotent, so a purge
// that stopped part way is finished by calling it again.
func (s *Store) DeleteTeam(ctx context.Context, team string) (Deleted, error) {
	if team == "" {
		return Deleted{}, fmt.Errorf("%w: DeleteTeam needs a team", ErrInvalidKey)
	}
	return s.DeletePrefix(ctx, team, "")
}

func isNotFound(err error) bool {
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}
