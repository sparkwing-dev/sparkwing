package logseg_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/blob"

	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type memObjects struct {
	mu   sync.Mutex
	objs map[string][]byte
	gets []string
	view store.LogSegments
}

func newMemObjects() *memObjects { return &memObjects{objs: map[string][]byte{}} }

func (m *memObjects) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets = append(m.gets, key)
	b, ok := m.objs[key]
	if !ok {
		return nil, blob.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memObjects) ReadLogSegments(_ context.Context, _ store.Actor, _ blob.LogReadRequest) (store.LogSegments, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	view := m.view
	view.Segments = append([]store.Segment(nil), view.Segments...)
	return view, nil
}

func (m *memObjects) put(key string, body []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = bytes.Clone(body)
}

func (m *memObjects) putIfAbsent(_ context.Context, key string, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objs[key]; ok {
		return logseg.ErrSealed
	}
	m.objs[key] = bytes.Clone(body)
	return nil
}

func (m *memObjects) getCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.gets)
}

type memUploader struct {
	objs   *memObjects
	prefix string

	mu      sync.Mutex
	failSeg func(n int) bool
	dead    bool
	stored  chan int
}

func newMemUploader(objs *memObjects, prefix string) *memUploader {
	objs.view.Subject = keySubject("acme", "run-1", "build", 1, 1)
	return &memUploader{objs: objs, prefix: prefix, stored: make(chan int, 1024)}
}

var errUnreachable = errors.New("object store unreachable")

func (u *memUploader) PutSegment(_ context.Context, seg logseg.Segment, body []byte) error {
	u.mu.Lock()
	refuse := u.dead || (u.failSeg != nil && u.failSeg(seg.N))
	u.mu.Unlock()
	if refuse {
		return errUnreachable
	}
	key := blob.LogSegmentKey(u.prefix, seg.N)
	u.objs.mu.Lock()
	u.objs.objs[key] = bytes.Clone(body)
	var offset int64
	for _, prior := range u.objs.view.Segments {
		offset += prior.Size
	}
	u.objs.view.Segments = append(u.objs.view.Segments, store.Segment{Index: int64(seg.N), Offset: offset, Size: seg.Bytes, StoredBytes: seg.StoredBytes, Digest: seg.SHA256, ObjectKey: key})
	u.objs.mu.Unlock()
	u.stored <- seg.N
	return nil
}

func (u *memUploader) PutManifest(ctx context.Context, m logseg.Manifest) error {
	u.mu.Lock()
	dead := u.dead
	u.mu.Unlock()
	if dead {
		return errUnreachable
	}
	raw, err := m.Marshal()
	if err != nil {
		return err
	}
	if err := u.objs.putIfAbsent(ctx, blob.LogManifestKey(u.prefix), raw); err != nil {
		return err
	}
	u.objs.mu.Lock()
	defer u.objs.mu.Unlock()
	u.objs.view.Subject = *m.Subject
	u.objs.view.Complete = m.Status == logseg.StatusComplete
	u.objs.view.IncompleteReason = m.Reason
	return nil
}

func (u *memUploader) kill() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.dead = true
}
