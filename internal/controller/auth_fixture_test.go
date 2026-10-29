package controller

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func newStoreForAuth(t *testing.T, opts ...store.Option) *store.Store {
	t.Helper()
	dir := t.TempDir()
	fixture := &serverFixture{}
	s, err := teststore.Open(filepath.Join(dir, "test.db"), append([]store.Option{fixture.option(deploymenttest.Metered(t), nil)}, opts...)...)
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// testClock reads real time until a test pins it, so fixture setup sees
// the same clock it saw before the pin.
type testClock struct {
	mu sync.Mutex
	at func() time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	at := c.at
	c.mu.Unlock()
	if at == nil {
		return time.Now()
	}
	return at()
}

func (c *testClock) pin(at func() time.Time) {
	c.mu.Lock()
	c.at = at
	c.mu.Unlock()
}

func (c *testClock) option() store.Option { return teststore.WithClock(c.now) }
