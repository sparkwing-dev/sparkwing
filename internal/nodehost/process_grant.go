package nodehost

import (
	"errors"

	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

// ProcessGrant transfers a delegated bearer and its idempotent revoker to the host.
type ProcessGrant struct {
	Binding nodeproto.ProcessBinding
	Revoke  func() error
}

func (g ProcessGrant) validate(parent nodeproto.ProcessBinding) error {
	if g.Revoke == nil {
		return errors.New("process binding requires a revocation callback")
	}
	return checkProcessBinding(parent, g.Binding)
}
