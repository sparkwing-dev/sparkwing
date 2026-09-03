package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// HostedAPIBaseURL is the host every hosted request is addressed to. The
// transport dials a unix socket and ignores it, but net/http still needs a
// syntactically whole URL.
const HostedAPIBaseURL = "http://sparkwing-api"

// safety: the daemon bounds a request at [APIRequestTimeout] and answers,
// so the client's own bound only has to outlast that answer; a shorter one
// would turn the daemon's error into a client timeout that names nothing.
const hostedAPITimeout = APIRequestTimeout + 15*time.Second

const hostedAPIProbeTimeout = 3 * time.Second

// NewAPISocketClient returns an HTTP client that reaches the daemon's
// controller API over the unix socket at sock. Requests carry no bearer
// token: the daemon takes the connection's peer uid as the principal.
func NewAPISocketClient(sock string) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Client{
		Timeout: hostedAPITimeout,
		Transport: otelutil.WrapTransport(&http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", sock)
			},
			MaxIdleConns:          16,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: hostedAPITimeout,
		}),
	}
}

// HostedBackends are the backends of a local run whose state lives behind
// the daemon's API socket. The run opens no store: state and concurrency
// travel over sock, while logs and artifacts stay on this machine's own
// files as they are for a run that opens the store directly.
func HostedBackends(paths Paths, sock string, art storage.ArtifactStore) Backends {
	httpClient := NewAPISocketClient(sock)
	return Backends{
		State:             client.New(HostedAPIBaseURL, httpClient),
		Logs:              localLogs{paths: paths},
		Concurrency:       NewHTTPConcurrency(HostedAPIBaseURL, httpClient, "", store.DefaultConcurrencyLease),
		Artifact:          art,
		LocalCoordination: true,
		APISocket:         sock,
	}
}

// safety: the handshake is the only place the daemon's own answer about
// api.sock is available, so selection happens before anything opens a store
// and never mid-run: once a run row exists on the daemon, an API failure is
// a run failure through the client's retry policy.
func selectHostedAPI(ctx context.Context, adm *LocalAdmission) (string, string) {
	if adm == nil {
		return "", "this run does not use the admission daemon"
	}
	if allowUnadmitted() {
		return "", AllowUnadmittedEnv + "=1 skips the daemon"
	}
	cl, err := wingdclient.EnsureDaemon(ctx, adm.clientOptions())
	if err != nil {
		return "", err.Error()
	}
	defer func() { _ = cl.Close() }()
	if !cl.APIReady() {
		if reason := cl.APIError(); reason != "" {
			return "", reason
		}
		return "", fmt.Sprintf("the admission daemon (%s) serves no controller API socket", cl.DaemonVersion())
	}
	sock := cl.APISocket()
	if err := hostedAPIReachable(ctx, sock); err != nil {
		return "", err.Error()
	}
	return sock, ""
}

// safety: a daemon older than this pipeline serves api.sock but not every
// route the run needs, and answers 404 with the unsupported-route body. The
// probe asks before any state is written, because after that a missing route
// is a run failure rather than a reason to change backend.
func hostedAPIServesCoordination(ctx context.Context, sock string) error {
	ctx, cancel := context.WithTimeout(ctx, hostedAPIProbeTimeout)
	defer cancel()
	httpClient := NewAPISocketClient(sock)
	defer httpClient.CloseIdleConnections()
	_, err := client.New(HostedAPIBaseURL, httpClient).
		ListPendingTriggersForParent(ctx, coordinationProbeRunID)
	if errors.Is(err, client.ErrControllerLacksRoute) {
		return err
	}
	return nil
}

// safety: no run carries this id, so the probe reads an empty list from a
// daemon that serves the route and 404 from one that does not.
const coordinationProbeRunID = "sparkwing-route-probe"

// safety: one request before any state is written, because a socket the
// daemon advertised can still be gone, and a daemon older than this pipeline
// answers 404 on routes the run needs. Either way the run takes today's
// direct path instead of failing.
func hostedAPIReachable(ctx context.Context, sock string) error {
	ctx, cancel := context.WithTimeout(ctx, hostedAPIProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, HostedAPIBaseURL+"/api/v1/health", nil)
	if err != nil {
		return err
	}
	probe := NewAPISocketClient(sock)
	defer probe.CloseIdleConnections()
	resp, err := probe.Do(req)
	if err != nil {
		return fmt.Errorf("controller API on %s: %w", sock, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("the admission daemon on %s does not serve GET /api/v1/health", sock)
	}
	var health struct {
		Store string `json:"store"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil || health.Store != "ready" {
		return nil
	}
	return hostedAPIServesCoordination(ctx, sock)
}

func hostedBackendsForRun(ctx context.Context, paths Paths, opts *Options) Backends {
	if opts.State != nil || !runsOnMachineStore(opts, paths) {
		return Backends{}
	}
	sock, reason := selectHostedAPI(ctx, opts.Admission)
	if sock == "" {
		if opts.Admission != nil && !allowUnadmitted() {
			opts.Admission.logf("the admission daemon does not serve this run's state, so it opens %s directly: %s",
				paths.StateDB(), reason)
		}
		return Backends{}
	}
	return HostedBackends(paths, sock, nil)
}

// safety: the daemon stands in front of this machine's own runs store and
// nothing else, so a controller or object-store profile keeps its own
// backend even when a daemon is serving.
func runsOnMachineStore(opts *Options, paths Paths) bool {
	if opts.LocalOnly {
		return opts.DefaultStateDB == paths.StateDB()
	}
	spec, _, _ := effectiveSurfaceSpecs(opts.Profile, opts, opts.DefaultStateDB)
	return spec != nil && spec.Type == backends.TypeSQLite && spec.Path == paths.StateDB()
}
