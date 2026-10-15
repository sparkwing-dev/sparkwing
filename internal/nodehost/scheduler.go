package nodehost

import (
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func maskErr(m *secrets.Masker, err error) error {
	if err == nil || m == nil {
		return err
	}
	return maskHostError(err, m.Mask)
}

type discardSink struct{}

func (discardSink) Text(string, string) {}

func (discardSink) Record(nodeproto.FrameLog) {}
