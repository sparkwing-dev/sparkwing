package client

import (
	"context"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

// EngineOptions keeps credentials in the transport and permits api.sock dialing
// through HTTP. The client uses C2's route retry metadata and shared wire types.
type EngineOptions struct {
	URL    string
	HTTP   *http.Client
	Bearer func(context.Context) (string, error)
	// Launcher selects /launcher/claim; the credential must independently hold claims.launch.
	Launcher bool
}

// NewEngineClient supplies the same controller API to every fetched engine.
// It decodes errors once with apierr.DecodeInner, preserves unknown additive
// fields/codes, and branches on codes rather than treating every 409 as lease loss.
// Claim with one trigger kind and RunID uses /runs/{run}/claim, including here.
// Otherwise Launcher selects /launcher/claim and ordinary claims use /runners/claim.
// Here intake is never transparently retried: replay cannot reproduce HereBearer.
func NewEngineClient(options EngineOptions) (controllerwire.Controller, error) {
	panic("unimplemented: controller-wire")
}
