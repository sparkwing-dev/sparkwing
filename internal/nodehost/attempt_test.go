//go:build unix

package nodehost

import (
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

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
