package orchestrator

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type lockedBuffer struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	want   string
	seen   chan struct{}
	closed bool
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	if !b.closed && strings.Contains(b.buf.String(), b.want) {
		b.closed = true
		close(b.seen)
	}
	return n, err
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestHostedAdmitterNarratesAQueuedWait(t *testing.T) {
	home := wingdTestHome(t)
	startWingd(t, home, 1)
	out := &lockedBuffer{want: "you are next", seen: make(chan struct{})}
	h := &hostedAdmitter{la: testWingdAdmission(home, out), pipeline: "deploy"}
	ctx, cancel := context.WithTimeout(t.Context(), wingdTestWait)
	defer cancel()

	holder, err := h.Acquire(ctx, wingwire.AdmissionRequest{RunID: "run-holder", Pipeline: "build", Resources: wingwire.HostResources{Cores: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	granted := make(chan error, 1)
	go func() {
		lease, err := h.Acquire(ctx, wingwire.AdmissionRequest{RunID: "run-waiter", Pipeline: "deploy", Resources: wingwire.HostResources{Cores: 1}}, nil)
		if err == nil {
			err = lease.Release()
		}
		granted <- err
	}()
	select {
	case <-out.seen:
	case <-ctx.Done():
		t.Fatalf("no queue narration while waiting:\n%s", out.String())
	}
	if err := holder.Release(); err != nil {
		t.Fatal(err)
	}
	if err := <-granted; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "admitted; starting run: participant run-waiter") {
		t.Errorf("no admitted line after the wait:\n%s", out.String())
	}
}
