package nodehost

import (
	"errors"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

// ProcessGrant transfers a delegated bearer and its idempotent revoker to the host.
type ProcessGrant struct {
	masks   *MaskRegistry
	Binding nodewire.ProcessBinding
	Revoke  func() error
}

func (g ProcessGrant) validate(parent nodewire.ProcessBinding) error {
	if g.Revoke == nil {
		return errors.New("process binding requires a revocation callback")
	}
	return checkProcessBinding(parent, g.Binding)
}
