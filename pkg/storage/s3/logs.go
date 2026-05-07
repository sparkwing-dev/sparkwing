package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/storage"
)

// LogStore implements storage.LogStore using rolling NDJSON objects:
// each Append creates a new object whose key is monotonic in time +
// process-local sequence. Reads ListObjectsV2 by node prefix and
// concatenate.
type LogStore struct {
	Bucket string
	Prefix string
	Client API

	// seq disambiguates Appends that land in the same nanosecond.
	seq atomic.Uint64
}

// NewLogStore wires a LogStore around the provided client.
func NewLogStore(bucket, prefix string, client API) *LogStore {
	return &LogStore{
		Bucket: bucket,
		Prefix: strings.TrimSuffix(prefix, "/"),
		Client: client,
	}
}

var _ storage.LogStore = (*LogStore)(nil)

func (s *LogStore) runPrefix(runID string) string {
	if s.Prefix == "" {
		return runID + "/"
	}
	return s.Prefix + "/" + runID + "/"
}

func (s *LogStore) nodePrefix(runID, nodeID string) string {
	return s.runPrefix(runID) + nodeID + "/"
}

func (s *LogStore) appendKey(runID, nodeID string) string {
	// 20-digit zero-padded ns + seq makes keys lex-sortable.
	now := time.Now().UTC().UnixNano()
	n := s.seq.Add(1)
	return fmt.Sprintf("%s%020d-%010d.ndjson", s.nodePrefix(runID, nodeID), now, n)
}

func (s *LogStore) Append(ctx context.Context, runID, nodeID string, data []byte) error {
	if runID == "" || nodeID == "" {
		return errors.New("s3.LogStore.Append: runID and nodeID required")
	}
	// Ensure trailing newline so Read never glues records onto one line.
	if len(data) > 0 && data[len(data)-1] != '\n' {
		buf := make([]byte, len(data)+1)
		copy(buf, data)
		buf[len(data)] = '\n'
		data = buf
	}
	_, err := s.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.Bucket),
		Key:         aws.String(s.appendKey(runID, nodeID)),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/x-ndjson"),
	})
	if err != nil {
		return fmt.Errorf("s3 logs append %s/%s: %w", runID, nodeID, err)
	}
	return nil
}

func (s *LogStore) Read(ctx context.Context, runID, nodeID string, opts storage.ReadOpts) ([]byte, error) {
	parts, err := s.listAndConcat(ctx, s.nodePrefix(runID, nodeID))
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, nil
	}
	return applyReadOpts(parts, opts)
}

func (s *LogStore) ReadRun(ctx context.Context, runID string) ([]byte, error) {
	prefix := s.runPrefix(runID)
	keys, err := s.listKeys(ctx, prefix)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, nil
	}
	byNode := map[string][]string{}
	for _, k := range keys {
		rest := strings.TrimPrefix(k, prefix)
		slash := strings.Index(rest, "/")
		if slash < 0 {
			continue
		}
		node := rest[:slash]
		byNode[node] = append(byNode[node], k)
	}
	// Stable order so output doesn't reshuffle between calls.
	nodes := make([]string, 0, len(byNode))
	for n := range byNode {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)

	var buf bytes.Buffer
	for _, n := range nodes {
		fmt.Fprintf(&buf, "=== %s ===\n", n)
		for _, k := range byNode[n] {
			data, err := s.getObject(ctx, k)
			if err != nil {
				return nil, err
			}
			buf.Write(data)
			if len(data) > 0 && data[len(data)-1] != '\n' {
				buf.WriteByte('\n')
			}
		}
	}
	return buf.Bytes(), nil
}

// Stream is unsupported for the S3 backend; callers fall back to
// polling Read.
func (s *LogStore) Stream(context.Context, string, string) (io.ReadCloser, error) {
	return nil, nil
}

func (s *LogStore) DeleteRun(ctx context.Context, runID string) error {
	keys, err := s.listKeys(ctx, s.runPrefix(runID))
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	// DeleteObjects caps at 1000 keys per request.
	for start := 0; start < len(keys); start += 1000 {
		end := start + 1000
		if end > len(keys) {
			end = len(keys)
		}
		objs := make([]s3types.ObjectIdentifier, 0, end-start)
		for _, k := range keys[start:end] {
			objs = append(objs, s3types.ObjectIdentifier{Key: aws.String(k)})
		}
		_, err := s.Client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.Bucket),
			Delete: &s3types.Delete{Objects: objs, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return fmt.Errorf("s3 logs delete-run %s: %w", runID, err)
		}
	}
	return nil
}

func (s *LogStore) listKeys(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	var token *string
	for {
		out, err := s.Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.Bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("s3 list %s: %w", prefix, err)
		}
		for _, o := range out.Contents {
			if o.Key != nil {
				keys = append(keys, *o.Key)
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		token = out.NextContinuationToken
	}
	// Lex order matches timestamp+seq key format -> chronological.
	sort.Strings(keys)
	return keys, nil
}

func (s *LogStore) listAndConcat(ctx context.Context, prefix string) ([]byte, error) {
	keys, err := s.listKeys(ctx, prefix)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for _, k := range keys {
		data, err := s.getObject(ctx, k)
		if err != nil {
			return nil, err
		}
		buf.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes(), nil
}

func (s *LogStore) getObject(ctx context.Context, key string) ([]byte, error) {
	out, err := s.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("s3 get %s: %w", key, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", key, err)
	}
	return data, nil
}

// applyReadOpts mirrors the filter semantics in pkg/storage/fs.
func applyReadOpts(data []byte, opts storage.ReadOpts) ([]byte, error) {
	if (opts == storage.ReadOpts{}) {
		return data, nil
	}
	lines := splitLines(data)

	if opts.Lines != "" {
		from, to, err := parseRange(opts.Lines)
		if err != nil {
			return nil, err
		}
		if from < 1 {
			from = 1
		}
		if to > len(lines) {
			to = len(lines)
		}
		if from > len(lines) || from > to {
			lines = nil
		} else {
			lines = lines[from-1 : to]
		}
	}

	if opts.Grep != "" {
		filtered := lines[:0]
		for _, l := range lines {
			if strings.Contains(l, opts.Grep) {
				filtered = append(filtered, l)
			}
		}
		lines = filtered
	}

	if opts.Tail > 0 && len(lines) > opts.Tail {
		lines = lines[len(lines)-opts.Tail:]
	}
	if opts.Head > 0 && len(lines) > opts.Head {
		lines = lines[:opts.Head]
	}

	var out bytes.Buffer
	for _, l := range lines {
		out.WriteString(l)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	s := string(data)
	if s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	return strings.Split(s, "\n")
}

func parseRange(spec string) (from, to int, err error) {
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid lines range %q", spec)
	}
	if _, err := fmt.Sscanf(parts[0], "%d", &from); err != nil {
		return 0, 0, fmt.Errorf("invalid lines.from %q: %w", parts[0], err)
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &to); err != nil {
		return 0, 0, fmt.Errorf("invalid lines.to %q: %w", parts[1], err)
	}
	return from, to, nil
}
