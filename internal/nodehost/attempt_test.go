//go:build unix

package nodehost

import (
	"context"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const fixtureToken = "hunter2-fixture-token"

type fixtureResolver struct{}

func (fixtureResolver) Resolve(_ context.Context, name string) (string, bool, error) {
	if name == "DEPLOY_TOKEN" {
		return fixtureToken, true, nil
	}
	return "", false, secretsource.ErrMissing
}

type memLogs struct {
	mu      sync.Mutex
	records map[string][]sparkwing.LogRecord
	open    int
}

type memLog struct {
	logs *memLogs
	node string
}

func (l memLog) Emit(rec sparkwing.LogRecord) {
	l.logs.mu.Lock()
	defer l.logs.mu.Unlock()
	l.logs.records[l.node] = append(l.logs.records[l.node], rec)
}

func (l memLog) Close() error {
	l.logs.mu.Lock()
	defer l.logs.mu.Unlock()
	l.logs.open--
	return nil
}

func (m *memLogs) text(node string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	for _, r := range m.records[node] {
		b.WriteString(r.Msg + "\n")
	}
	return b.String()
}

// safety: masks each line when it arrives, as a real plan sink does, so a
// value registered too late shows in the text.
type planSinkText struct {
	masker *secrets.Masker
	mu     sync.Mutex
	text   strings.Builder
}

func (p *planSinkText) Text(_, line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.text.WriteString(p.masker.Mask(line) + "\n")
}

func (p *planSinkText) Record(rec nodeproto.FrameLog) { p.Text("", rec.Message) }

func (p *planSinkText) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.text.String()
}
