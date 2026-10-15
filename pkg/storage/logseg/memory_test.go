package logseg_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/blob"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
)

type memObjects struct {
	mu   sync.Mutex
	objs map[string][]byte
	gets []string
}

func newMemObjects() *memObjects { return &memObjects{objs: map[string][]byte{}} }

func (m *memObjects) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets = append(m.gets, key)
	b, ok := m.objs[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memObjects) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
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

func (m *memObjects) deletePrefix(prefix string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.objs {
		if strings.HasPrefix(k, prefix) {
			delete(m.objs, k)
		}
	}
}

func (m *memObjects) resetGets() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets = nil
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
	u.objs.put(blob.LogSegmentKey(u.prefix, seg.N), body)
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
	return u.objs.putIfAbsent(ctx, blob.LogManifestKey(u.prefix), raw)
}

func (u *memUploader) kill() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.dead = true
}
