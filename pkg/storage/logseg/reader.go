package logseg

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// Objects is the read side of the store a log's objects live in.
type Objects interface {
	// Get returns the object at key, or an error wrapping
	// storage.ErrNotFound.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// List returns every key under prefix.
	List(ctx context.Context, prefix string) ([]string, error)
}

// safety: a segment covers at most SegmentBytes of raw log plus one append, so anything larger is not a segment.
const maxSegmentStored = 8 << 20

// Log is one node attempt's log as a reader finds it.
type Log struct {
	Prefix string
	// Manifest is nil while the attempt is unsealed.
	Manifest *Manifest
	// Segments are the manifest's, or for an unsealed attempt the dense
	// run of segments listed from 1, which carry only their number.
	Segments []Segment
	// Broken is the first segment a read found missing or failing its
	// digest; zero when every segment read verified.
	Broken int
}

// Open finds an attempt's manifest, or lists its segments when it has
// none yet.
func Open(ctx context.Context, objs Objects, prefix string) (*Log, error) {
	l := &Log{Prefix: prefix}
	raw, err := readObject(ctx, objs, ManifestKey(prefix), maxSegmentStored)
	switch {
	case err == nil:
		m, perr := ParseManifest(raw)
		if perr != nil {
			return nil, perr
		}
		l.Manifest, l.Segments = m, m.Segments
		return l, nil
	case !errors.Is(err, storage.ErrNotFound):
		return nil, err
	}
	l.Segments, err = listSegments(ctx, objs, prefix)
	return l, err
}

func listSegments(ctx context.Context, objs Objects, prefix string) ([]Segment, error) {
	keys, err := objs.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	var nums []int
	for _, k := range keys {
		if n, ok := SegmentNumber(prefix, k); ok {
			nums = append(nums, n)
		}
	}
	slices.Sort(nums)
	var segs []Segment
	for i, n := range nums {
		if n != i+1 {
			break
		}
		segs = append(segs, Segment{N: n})
	}
	return segs, nil
}

// Read returns the log's lines narrowed by opts. A segment that is
// missing or fails its digest ends the read there and sets Broken; the
// lines before it are returned.
func (l *Log) Read(ctx context.Context, objs Objects, opts storage.ReadOpts) ([]byte, error) {
	segs := l.Segments
	if tail, ok := tailOnly(opts); ok && l.Manifest != nil {
		var lines int64
		i := len(segs)
		for i > 0 && lines < int64(tail) {
			i--
			lines += segs[i].Lines
		}
		segs = segs[i:]
	}
	var out bytes.Buffer
	for _, seg := range segs {
		body, err := l.segment(ctx, objs, seg)
		if errors.Is(err, errBadSegment) || errors.Is(err, storage.ErrNotFound) {
			l.Broken = seg.N
			break
		}
		if err != nil {
			return nil, err
		}
		out.Write(body)
	}
	return opts.Apply(out.Bytes())
}

func tailOnly(opts storage.ReadOpts) (int, bool) {
	return opts.Tail, opts.Tail > 0 && opts == storage.ReadOpts{Tail: opts.Tail}
}

var errBadSegment = errors.New("logseg: segment does not match its manifest")

func (l *Log) segment(ctx context.Context, objs Objects, seg Segment) ([]byte, error) {
	stored, err := readObject(ctx, objs, SegmentKey(l.Prefix, seg.N), maxSegmentStored)
	if err != nil {
		return nil, err
	}
	return decodeSegment(stored, seg)
}

func decodeSegment(stored []byte, seg Segment) ([]byte, error) {
	if seg.SHA256 != "" {
		sum := sha256.Sum256(stored)
		if hex.EncodeToString(sum[:]) != seg.SHA256 {
			return nil, fmt.Errorf("%w: segment %d digest", errBadSegment, seg.N)
		}
	}
	zr, err := gzip.NewReader(bytes.NewReader(stored))
	if err != nil {
		return nil, fmt.Errorf("%w: segment %d: %w", errBadSegment, seg.N, err)
	}
	body, err := io.ReadAll(io.LimitReader(zr, maxSegmentStored+1))
	if err != nil {
		return nil, fmt.Errorf("%w: segment %d: %w", errBadSegment, seg.N, err)
	}
	if len(body) > maxSegmentStored {
		return nil, fmt.Errorf("%w: segment %d decompresses past %d bytes", errBadSegment, seg.N, maxSegmentStored)
	}
	return body, nil
}

func readObject(ctx context.Context, objs Objects, key string, limit int64) ([]byte, error) {
	rc, err := objs.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", errBadSegment, key, limit)
	}
	return raw, nil
}

// Completeness judges the log after a read: the manifest's verdict, or
// incomplete naming the first segment that did not verify.
func (l *Log) Completeness(node NodeProgress, now time.Time) Completeness {
	c := Assess(l.Manifest, node, now)
	if l.Broken == 0 {
		return c
	}
	c.State = StateIncomplete
	c.Message = fmt.Sprintf("logs incomplete: segment %d is missing or does not match its digest; the log stops before it", l.Broken)
	return c
}

// SealAbandoned writes the manifest for an attempt whose node never
// sealed it: the dense run of segments it finds, read and digested,
// with status incomplete and the given reason. An attempt that already
// has a manifest keeps it, and SealAbandoned returns it. put must
// create the manifest only if none exists and return ErrSealed
// otherwise.
func SealAbandoned(ctx context.Context, objs Objects, prefix string, base Manifest, reason string,
	put func(ctx context.Context, key string, body []byte) error,
) (*Manifest, error) {
	found, err := Open(ctx, objs, prefix)
	if err != nil {
		return nil, err
	}
	if found.Manifest != nil {
		return found.Manifest, nil
	}
	segs := found.Segments
	m := base
	m.Version, m.Status, m.Reason = ManifestVersion, StatusIncomplete, reason
	m.SealedBy, m.SealedAt = SealedByController, time.Now().UTC()
	m.Segments, m.Lines, m.Bytes, m.FirstSeq, m.LastSeq = nil, 0, 0, 0, 0
	for _, seg := range segs {
		stored, err := readObject(ctx, objs, SegmentKey(prefix, seg.N), maxSegmentStored)
		if err != nil {
			return nil, err
		}
		body, err := decodeSegment(stored, Segment{N: seg.N})
		if err != nil {
			break
		}
		sum := sha256.Sum256(stored)
		lines := int64(bytes.Count(body, []byte{'\n'}))
		m.Segments = append(m.Segments, Segment{
			N: seg.N, FirstSeq: m.Lines + 1, LastSeq: m.Lines + lines, Lines: lines,
			Bytes: int64(len(body)), StoredBytes: int64(len(stored)), SHA256: hex.EncodeToString(sum[:]),
		})
		m.Lines += lines
		m.Bytes += int64(len(body))
	}
	if m.Lines > 0 {
		m.FirstSeq, m.LastSeq = 1, m.Lines
	}
	raw, err := m.Marshal()
	if err != nil {
		return nil, err
	}
	if err := put(ctx, ManifestKey(prefix), raw); errors.Is(err, ErrSealed) {
		l, oerr := Open(ctx, objs, prefix)
		if oerr != nil {
			return nil, oerr
		}
		if l.Manifest == nil {
			return nil, fmt.Errorf("logseg: %s reported sealed but has no manifest", prefix)
		}
		return l.Manifest, nil
	} else if err != nil {
		return nil, err
	}
	return &m, nil
}
