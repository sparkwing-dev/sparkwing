package client

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
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
	return newEngineClient(options)
}

// NewRunReader uses the authenticated controller transport for public run views.
func NewRunReader(options EngineOptions) (controllerwire.RunReader, error) {
	return newEngineClient(options)
}

func newEngineClient(options EngineOptions) (*engineClient, error) {
	base, err := url.Parse(options.URL)
	if err != nil || base == nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, &apierr.Error{Code: apierr.BadRequest, Message: "controller URL must be an HTTP(S) base URL without credentials, query or fragment"}
	}
	hc := http.Client{Timeout: 30 * time.Second}
	if options.HTTP != nil {
		hc = *options.HTTP
	}
	// safety: credentials and private execution inputs must stay at the selected controller.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &engineClient{base: strings.TrimRight(base.String(), "/"), http: &hc, bearer: options.Bearer, launcher: options.Launcher}, nil
}
