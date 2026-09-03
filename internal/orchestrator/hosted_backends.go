package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/mod/semver"

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

// safety: outlasts the answer the daemon bounds at [APIRequestTimeout], since
// shorter would abandon a request it is about to answer. Longer than
// [HostedRestartBudget] on purpose: that budget buys back a daemon that
// refuses or restarts, while one that accepts and never answers ends here.
const hostedAPITimeout = APIRequestTimeout + 15*time.Second

const hostedAPIProbeTimeout = 3 * time.Second

// safety: the two ways a daemon's store answer ends a hosted run. Skew is the
// daemon being too old for a store a newer pin migrated, which is age and so
// degrades; a fault is a file the operator must fix, and is the only reason
// left to refuse a run outright.
var (
	errHostedStoreSkew  = errors.New("the admission daemon is too old for this machine's runs store")
	errHostedStoreFault = errors.New("the admission daemon cannot read this machine's runs store")
)

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

type hostedSelection struct {
	sock       string
	daemon     string
	standalone string
	fault      string
	storeSkew  bool
}

// safety: the handshake is the only place the daemon's own answer about
// api.sock is available, so selection happens before anything opens a store
// and never mid-run: once a run row exists on the daemon, an API failure is
// a run failure through the client's retry policy.
func selectHostedAPI(ctx context.Context, adm *LocalAdmission) (hostedSelection, error) {
	// safety: a run with no admission configured is an embedder driving the
	// orchestrator, not a pipeline binary answering to this machine's daemon,
	// so it keeps the store it was pointed at.
	if adm == nil {
		return hostedSelection{}, nil
	}
	cl, err := wingdclient.EnsureDaemon(ctx, adm.clientOptions())
	if err != nil {
		if reason := standaloneReasonFor(err); reason != "" {
			return hostedSelection{daemon: wingdclient.DaemonVersionOf(err), standalone: reason}, nil
		}
		return hostedSelection{}, err
	}
	defer func() { _ = cl.Close() }()
	sel := hostedSelection{daemon: cl.DaemonVersion()}
	// safety: the operator asked for the direct path with a daemon answering,
	// so the run says that rather than a sentence about a daemon that is not
	// there. Asked after the handshake because only it proves which is true.
	if allowUnadmitted() {
		sel.standalone = standaloneForced
		return sel, nil
	}
	if !cl.APIReady() {
		sel.standalone, sel.fault = daemonAPIRefusal(cl)
		return sel, nil
	}
	sock := cl.APISocket()
	if err := hostedAPIReachable(ctx, sock); err != nil {
		if errors.Is(err, errHostedStoreFault) {
			return hostedSelection{}, err
		}
		if reason := standaloneReasonFor(err); reason != "" {
			sel.standalone = reason
			sel.storeSkew = errors.Is(err, errHostedStoreSkew)
			return sel, nil
		}
		sel.standalone, sel.fault = standaloneDaemonFault, err.Error()
		return sel, nil
	}
	sel.sock = sock
	return sel, nil
}

// safety: a daemon that never advertised api_ready predates the field, which
// is age; one that advertises it false bound no socket, which is a fault of
// this machine's daemon and whose own reason the operator needs verbatim.
func daemonAPIRefusal(cl *wingdclient.Client) (reason, fault string) {
	if !cl.APIAdvertised() {
		return standaloneDaemonOlder, ""
	}
	detail := cl.APIError()
	if detail == "" {
		detail = "it advertises no controller API socket"
	}
	if supersedesSDK(sparkwingModuleVersion(), cl.DaemonVersion()) {
		return standaloneDaemonOlder, detail
	}
	return standaloneDaemonFault, detail
}

// safety: only a comparable pair proves age, and only across releases: a local
// build of the same release carries a describe or prerelease suffix that sorts
// below the tag, which is not the daemon being behind. Two builds that cannot
// be ordered read as a fault and keep the daemon's own reason.
func supersedesSDK(sdk, daemon string) bool {
	sdkBase, daemonBase := releaseBase(sdk), releaseBase(daemon)
	if sdkBase == "" || daemonBase == "" || sdkBase == daemonBase {
		return false
	}
	return semver.Compare(daemonBase, sdkBase) < 0
}

func releaseBase(v string) string {
	v = bareVersion(v)
	if !semver.IsValid(v) {
		return ""
	}
	base, _, _ := strings.Cut(v, "+")
	base, _, _ = strings.Cut(base, "-")
	if !semver.IsValid(base) {
		return ""
	}
	return semver.Canonical(base)
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
	if decodeErr == nil {
		if reason, skewed := strings.CutPrefix(health.Store, "skew: "); skewed {
			return fmt.Errorf("%w: %s reports %s", errHostedStoreSkew, sock, reason)
		}
	}
	if resp.StatusCode != http.StatusOK {
		if decodeErr == nil && health.Store != "" {
			return fmt.Errorf("%w: %s answered %s for GET /api/v1/health with its runs store %q",
				errHostedStoreFault, sock, resp.Status, health.Store)
		}
		return fmt.Errorf("%s answered %s for GET /api/v1/health", sock, resp.Status)
	}
	if decodeErr != nil {
		return fmt.Errorf("%s did not answer GET /api/v1/health with a health report: %w", sock, decodeErr)
	}
	switch health.Store {
	case "ready", "absent":
	default:
		return fmt.Errorf("%w: the daemon on %s reports its runs store %q", errHostedStoreFault, sock, health.Store)
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

func hostedBackendsForRun(ctx context.Context, paths Paths, opts *Options) (Backends, hostedSelection, func(), error) {
	noop := func() {}
	if opts.State != nil || !runsOnMachineStore(opts, paths) {
		return Backends{}, hostedSelection{}, noop, nil
	}
	sel, err := selectHostedAPI(ctx, opts.Admission)
	if err != nil {
		return Backends{}, hostedSelection{}, noop, err
	}
	if sel.sock == "" {
		return Backends{}, sel, noop, nil
	}
	hosted, release := HostedBackends(paths, sel.sock, nil)
	return hosted, sel, release, nil
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
