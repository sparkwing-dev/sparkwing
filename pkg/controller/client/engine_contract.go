package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

// EngineOptions keeps credentials in the transport and permits api.sock dialing
// through HTTP. The client uses C2's route retry metadata and shared wire types.
type EngineOptions struct {
	URL    string
	HTTP   *http.Client
	Bearer func(context.Context) (string, error)
	// Launcher selects the deployment launcher claim route, including node-only
	// and named selections. The server still authenticates the credential's grants.
	Launcher bool
}

// NewEngineClient supplies the same controller API to every fetched engine.
// It decodes errors once with apierr.DecodeInner, accepts additive fields and
// unknown codes, and branches on codes rather than treating every 409 as lease loss.
func NewEngineClient(options EngineOptions) (controllerwire.Controller, error) {
	base, err := url.Parse(options.URL)
	if err != nil || base == nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("controller URL must be an HTTP(S) base URL without credentials, query or fragment")
	}
	hc := http.Client{Timeout: 30 * time.Second}
	if options.HTTP != nil {
		hc = *options.HTTP
	}
	// safety: credentials and private execution inputs must stay at the selected controller.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &engineClient{base: strings.TrimRight(base.String(), "/"), http: &hc, bearer: options.Bearer, launcher: options.Launcher, decodeError: apierr.DecodeInner}, nil
}
