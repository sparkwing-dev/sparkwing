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

// safety: marks the reasons admission does not report for itself. A daemon
// this pipeline could not reach at all is admission's own subject.
var errNoHostedAPI = errors.New("the admission daemon does not serve this run's state")

// NewAPISocketClient returns an HTTP client that reaches the daemon's
// controller API over the unix socket at sock. Requests carry no bearer
// token: the daemon takes the connection's peer uid as the principal. A
// write the daemon can still be shown to have missed is retried across a
// daemon restart; see [HostedRestartBudget].
func NewAPISocketClient(sock string) *http.Client {
	return &http.Client{
		Timeout:   hostedAPITimeout,
		Transport: otelutil.WrapTransport(newHostedRetryTransport(apiSocketTransport(sock))),
	}
}

// safety: selection must read the daemon's answer, not wait it out. The
// retrying client exists so a run outlives a restart; a probe that retried a
// 503 would spend its whole budget on a daemon that already said no.
func newAPIProbeClient(sock string) *http.Client {
	return &http.Client{Timeout: hostedAPIProbeTimeout, Transport: apiSocketTransport(sock)}
}

func apiSocketTransport(sock string) *http.Transport {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", sock)
		},
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: hostedAPITimeout,
	}
}

// HostedBackends are the backends of a local run whose state lives behind
// the daemon's API socket. The run opens no store: state and concurrency
// travel over sock, while logs and artifacts stay on this machine's own
// files as they are for a run that opens the store directly. The returned
// function releases the connections the run held.
func HostedBackends(paths Paths, sock string, art storage.ArtifactStore) (Backends, func()) {
	httpClient := NewAPISocketClient(sock)
	return Backends{
		State:             client.New(HostedAPIBaseURL, httpClient),
		Logs:              localLogs{paths: paths},
		Concurrency:       NewHTTPConcurrency(HostedAPIBaseURL, httpClient, "", store.DefaultConcurrencyLease),
		Artifact:          art,
		LocalCoordination: true,
		APISocket:         sock,
	}, httpClient.CloseIdleConnections
}

// safety: the handshake is the only place the daemon's own answer about
// api.sock is available, so selection happens before anything opens a store
// and never mid-run: once a run row exists on the daemon, an API failure is
// a run failure through the client's retry policy.
func selectHostedAPI(ctx context.Context, adm *LocalAdmission) (string, error) {
	if adm == nil {
		return "", fmt.Errorf("%w: this run does not use the admission daemon", errNoHostedAPI)
	}
	if allowUnadmitted() {
		return "", fmt.Errorf("%w: %s=1 skips the daemon", errNoHostedAPI, AllowUnadmittedEnv)
	}
	cl, err := wingdclient.EnsureDaemon(ctx, adm.clientOptions())
	if err != nil {
		return "", err
	}
	defer func() { _ = cl.Close() }()
	if !cl.APIReady() {
		reason := cl.APIError()
		if reason == "" {
			reason = fmt.Sprintf("the daemon (%s) advertises no controller API socket", cl.DaemonVersion())
		}
		return "", fmt.Errorf("%w: %s", errNoHostedAPI, reason)
	}
	sock := cl.APISocket()
	if err := hostedAPIReachable(ctx, sock); err != nil {
		return "", fmt.Errorf("%w: %w", errNoHostedAPI, err)
	}
	return sock, nil
}

type hostedHealth struct {
	Store string `json:"store"`
}

// safety: only a daemon answering 200 with a store it can open, or with none
// yet, hosts a run. Every other answer is a daemon this run must not bind
// to, because the process that could still open that file is this one.
func hostedAPIReachable(ctx context.Context, sock string) error {
	ctx, cancel := context.WithTimeout(ctx, hostedAPIProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, HostedAPIBaseURL+"/api/v1/health", nil)
	if err != nil {
		return err
	}
	probe := newAPIProbeClient(sock)
	defer probe.CloseIdleConnections()
	resp, err := probe.Do(req)
	if err != nil {
		return fmt.Errorf("controller API on %s: %w", sock, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	var health hostedHealth
	decodeErr := json.NewDecoder(resp.Body).Decode(&health)
	if resp.StatusCode != http.StatusOK {
		if decodeErr == nil && health.Store != "" {
			return fmt.Errorf("%s answered %s for GET /api/v1/health with its runs store %q",
				sock, resp.Status, health.Store)
		}
		return fmt.Errorf("%s answered %s for GET /api/v1/health", sock, resp.Status)
	}
	if decodeErr != nil {
		return fmt.Errorf("%s did not answer GET /api/v1/health with a health report: %w", sock, decodeErr)
	}
	switch health.Store {
	case "ready", "absent":
	default:
		return fmt.Errorf("the daemon on %s reports its runs store %q", sock, health.Store)
	}
	return hostedAPIServesCoordination(ctx, sock)
}

// safety: one GET per coordination route family, so the probe writes
// nothing. No run and no pipeline carries these ids, so a daemon that serves
// the route answers empty and one that does not answers the unsupported
// body, whatever state its store is in.
var hostedCoordinationProbes = []func(context.Context, *client.Client) error{
	func(ctx context.Context, c *client.Client) error {
		_, err := c.ListPendingTriggersForParent(ctx, hostedProbeID)
		return err
	},
	func(ctx context.Context, c *client.Client) error {
		_, err := c.GetTrigger(ctx, hostedProbeID)
		return err
	},
	func(ctx context.Context, c *client.Client) error {
		_, err := c.GetPipelineProfile(ctx, hostedProbeID, "")
		return err
	},
	func(ctx context.Context, c *client.Client) error {
		_, err := c.ListNodeMetrics(ctx, hostedProbeID, hostedProbeID)
		return err
	},
}

const hostedProbeID = "sparkwing-route-probe"

// safety: a daemon older than this pipeline serves api.sock but not every
// route, and the trigger loop's failure is otherwise silent while the run
// reports success. Asked before any state is written, because after that a
// missing route is a run failure rather than a reason to change backend.
func hostedAPIServesCoordination(ctx context.Context, sock string) error {
	httpClient := newAPIProbeClient(sock)
	defer httpClient.CloseIdleConnections()
	c := client.New(HostedAPIBaseURL, httpClient)
	for _, probe := range hostedCoordinationProbes {
		if err := probe(ctx, c); errors.Is(err, client.ErrControllerLacksRoute) {
			return err
		}
	}
	return nil
}

func hostedBackendsForRun(ctx context.Context, paths Paths, opts *Options) (Backends, func()) {
	noop := func() {}
	if opts.State != nil || !runsOnMachineStore(opts, paths) {
		return Backends{}, noop
	}
	sock, err := selectHostedAPI(ctx, opts.Admission)
	if err != nil {
		// safety: a daemon this run could not reach at all is admission's own
		// subject and it prints its own line, so only a reason admission never
		// sees is announced here. Two lines for one condition is the thing
		// the design's single stderr warning replaces.
		if opts.Admission != nil && errors.Is(err, errNoHostedAPI) && !allowUnadmitted() {
			opts.Admission.logf("%s, so it opens %s directly", err, paths.StateDB())
		}
		return Backends{}, noop
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
