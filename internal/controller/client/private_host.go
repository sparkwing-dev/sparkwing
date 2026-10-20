package client

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/url"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// LocalHostClient binds private host state and authority reads to one awarded run.
type LocalHostClient struct {
	transport *engineClient
	run       string
	node      string
	mu        sync.Mutex
	nodes     map[string]localNodeBearer
	closed    bool
}

type localNodeBearer struct {
	raw        string
	prefix     string
	unit       string
	generation int64
}

// ReadLocalPublication exports terminal local state under operating-system peer authority.
func ReadLocalPublication(ctx context.Context, options EngineOptions, run string) (store.LocalPublication, error) {
	c, err := newEngineClient(options)
	if err != nil {
		return store.LocalPublication{}, err
	}
	var result store.LocalPublication
	err = c.request(ctx, "GET", "/api/v1/runs/{run}/local-host/snapshot", "/api/v1/runs/"+url.PathEscape(run)+"/local-host/snapshot", "", nil, &result)
	if err == nil && result.Run.ID != run {
		err = localHostClientRefusal()
	}
	return result, err
}

// NewLocalHostClient uses the authenticated local controller API without opening state.
func NewLocalHostClient(options EngineOptions, runID string) (*LocalHostClient, error) {
	if runID == "" || options.Bearer == nil {
		return nil, errors.New("local host requires a run and awarded bearer")
	}
	c := &LocalHostClient{run: runID, nodes: make(map[string]localNodeBearer)}
	bearer := options.Bearer
	options.Bearer = func(ctx context.Context) (string, error) {
		if err := c.live(); err != nil {
			return "", err
		}
		raw, err := bearer(ctx)
		if err != nil {
			return "", err
		}
		if err := c.live(); err != nil {
			return "", err
		}
		return raw, nil
	}
	transport, err := newEngineClient(options)
	if err != nil {
		return nil, err
	}
	c.transport = transport
	return c, nil
}

// NewNodeHostClient confines private state and authority reads to one awarded node.
func NewNodeHostClient(options EngineOptions, runID, nodeID string) (*LocalHostClient, error) {
	if nodeID == "" {
		return nil, errors.New("node host requires its awarded node")
	}
	c, err := NewLocalHostClient(options, runID)
	if err == nil {
		c.node = nodeID
	}
	return c, err
}

func (c *LocalHostClient) request(ctx context.Context, method, suffix, node string, body, result any) error {
	if err := c.live(); err != nil {
		return err
	}
	pattern, path := "/api/v1/runs/{run}/local-host", "/api/v1/runs/"+url.PathEscape(c.run)+"/local-host"
	if c.node != "" {
		if node != "" && node != c.node {
			return localHostClientRefusal()
		}
		node = c.node
	}
	if node != "" {
		pattern, path = pattern+"/nodes/{job}", path+"/nodes/"+url.PathEscape(node)
	}
	return c.transport.request(ctx, method, pattern+suffix, path+suffix, "", body, result)
}

func (c *LocalHostClient) live() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("local host client is closed")
	}
	return nil
}

// ReadSnapshot reads the private execution snapshot under the current owner fence.
func (c *LocalHostClient) ReadSnapshot(ctx context.Context, run string) (store.RunSnapshot, error) {
	var result store.RunSnapshot
	if run != c.run {
		return result, localHostClientRefusal()
	}
	if err := c.request(ctx, "GET", "/snapshot", "", nil, &result); err != nil {
		return store.RunSnapshot{}, err
	}
	if result.Run.ID != c.run {
		return store.RunSnapshot{}, errors.New("local host snapshot names another run")
	}
	return result, nil
}

// ReadNode reads private execution state for one node of the awarded run.
func (c *LocalHostClient) ReadNode(ctx context.Context, ref controllerwire.NodeRef) (store.NodeRow, error) {
	var result store.NodeRow
	if ref.RunID != c.run || ref.NodeID == "" {
		return result, localHostClientRefusal()
	}
	if c.node != "" && ref.NodeID != c.node {
		var snapshot localhostwire.LocalNodeSnapshot
		if err := c.request(ctx, "GET", "/snapshot", "", nil, &snapshot); err != nil {
			return result, err
		}
		if snapshot.Run.ID != c.run {
			return result, localHostClientRefusal()
		}
		if key, ok := snapshot.ArtifactSources[ref.NodeID]; ok {
			return store.NodeRow{Ref: ref, Status: store.StatusDone, ManifestRecord: &key}, nil
		}
		return result, localHostClientRefusal()
	}
	if err := c.request(ctx, "GET", "", ref.NodeID, nil, &result); err != nil {
		return store.NodeRow{}, err
	}
	if result.Ref != ref {
		return store.NodeRow{}, errors.New("local host node names another execution")
	}
	return result, nil
}

// TransitionRun submits an edge while the server derives its actor and fence.
func (c *LocalHostClient) TransitionRun(ctx context.Context, change store.RunTransition) (store.Applied, error) {
	var result store.Applied
	if c.node != "" || change.RunID != c.run {
		return result, localHostClientRefusal()
	}
	body := localhostwire.LocalRunTransition{To: change.To, Cause: change.Cause, Reason: change.Reason, MaskedError: change.MaskedError, OwnerPolicy: change.OwnerPolicy}
	err := c.request(ctx, "POST", "/transition", "", body, &result)
	return result, err
}

// FinishRunAndUnit commits the terminal policy verdict and owner completion atomically.
func (c *LocalHostClient) FinishRunAndUnit(ctx context.Context, finish store.FinishRunRequest) (store.Applied, error) {
	var result store.Applied
	change := finish.Transition
	if c.node != "" || change.RunID != c.run || finish.Unit != change.Actor.Owner {
		return result, localHostClientRefusal()
	}
	body := localhostwire.LocalRunTransition{To: change.To, Cause: change.Cause, Reason: change.Reason, MaskedError: change.MaskedError, OwnerPolicy: change.OwnerPolicy}
	err := c.request(ctx, "POST", "/finish", "", body, &result)
	return result, err
}

// TransitionNode selects retained holder credentials without sending actor authority.
func (c *LocalHostClient) TransitionNode(ctx context.Context, change store.NodeTransition) (store.Applied, error) {
	var result store.Applied
	if change.Ref.RunID != c.run || change.Ref.NodeID == "" || change.Extra.Claim != nil {
		return result, localHostClientRefusal()
	}
	selected := c
	if change.Actor.Kind == store.ActorHolder && c.node == "" {
		transport, award, err := c.nodeTransport(ctx, change.Ref)
		if err != nil {
			return result, err
		}
		if change.Actor.CapabilityPrefix != award.prefix || change.Actor.Generation != award.generation {
			return result, localHostClientRefusal()
		}
		selected = &LocalHostClient{transport: transport, run: c.run}
	}
	extra := change.Extra
	body := localhostwire.LocalNodeTransition{
		To: change.To, Cause: change.Cause, SourceAttempt: change.SourceAttempt,
		Extra: localhostwire.LocalTransitionExtra{OwnerPolicy: extra.OwnerPolicy, Result: extra.Result, Materialize: extra.Materialize, Approval: extra.Approval, RetryAt: extra.RetryAt},
	}
	err := selected.request(ctx, "POST", "/transition", change.Ref.NodeID, body, &result)
	return result, err
}

// ReadAuthentication refreshes only authority within the host's awarded run.
func (c *LocalHostClient) ReadAuthentication(ctx context.Context, lookup capability.CredentialLookup, evidence capability.RequestEvidence) (capability.AuthenticationSnapshot, error) {
	var response localhostwire.LocalAuthentication
	err := c.request(ctx, "POST", "/authentication", "", localhostwire.LocalAuthenticationRequest{Lookup: lookup, Evidence: evidence}, &response)
	if err != nil {
		return capability.AuthenticationSnapshot{}, err
	}
	if len(response.Digests) != len(response.Snapshot.Authorities) || len(response.Digests) == 0 || response.Digests[0] != lookup.Digest {
		return capability.AuthenticationSnapshot{}, errors.New("local authority snapshot has inconsistent digests")
	}
	for i, digest := range response.Digests {
		response.Snapshot.Authorities[i].Principal.Capability.Digest = digest
	}
	return response.Snapshot, nil
}

// CheckBinding asks the controller to resolve authority before checking persisted relations.
func (c *LocalHostClient) CheckBinding(ctx context.Context, principal capability.Principal, rule capability.BindingRule, target capability.Target) error {
	var result localhostwire.LocalBindingChecked
	if err := c.request(ctx, "POST", "/binding", "", localhostwire.LocalBindingRequest{Authority: principal.Authority, Rule: rule, Target: target}, &result); err != nil {
		return err
	}
	if !result.Checked {
		return errors.New("local binding check was not acknowledged")
	}
	return nil
}

// Principal authenticates the awarded owner with a fresh authority and liveness snapshot.
func (c *LocalHostClient) Principal(ctx context.Context) (capability.Principal, error) {
	if err := c.live(); err != nil {
		return capability.Principal{}, err
	}
	raw, err := c.transport.bearer(ctx)
	if err != nil {
		return capability.Principal{}, err
	}
	pattern := "/api/v1/claims/{claim}/heartbeat"
	route, ok := capability.LookupRoute("POST", pattern)
	if !ok {
		return capability.Principal{}, errors.New("owner heartbeat policy is unavailable")
	}
	authenticated, err := capability.NewAuthenticator(c).Authenticate(ctx, capability.Presentation{Class: capability.CredentialBearer, Raw: raw, Evidence: capability.RequestEvidence{Target: capability.Target{RunID: c.run, NodeID: c.node}}}, route)
	return authenticated.Principal, err
}

// AdoptClaim retains a separately awarded node bearer after fresh ownership checks.
func (c *LocalHostClient) AdoptClaim(ctx context.Context, claim store.ReconciledNode) error {
	if c.node != "" || claim.Kind != store.Execute || claim.Claim == nil || len(claim.Bearer) == 0 || claim.Node.Ref.RunID != c.run || claim.Node.Ref.NodeID == "" {
		return localHostClientRefusal()
	}
	owner, err := c.Principal(ctx)
	if err != nil {
		return err
	}
	row, err := c.ReadNode(ctx, claim.Node.Ref)
	if err != nil {
		return err
	}
	if row.CapabilityPrefix != claim.Claim.CapabilityPrefix || row.Generation != claim.Claim.Generation || row.ClaimedBy != claim.Claim.Holder || row.OwnerCapabilityPrefix != owner.Capability.Prefix || row.ClaimedBy != owner.Capability.Binding.Holder || row.Ref != claim.Node.Ref || row.Generation != claim.Node.Generation || row.CapabilityPrefix != claim.Node.CapabilityPrefix {
		return localHostClientRefusal()
	}
	raw := string(claim.Bearer)
	lookup := capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: sha256.Sum256(claim.Bearer)}
	snapshot, err := c.ReadAuthentication(ctx, lookup, capability.RequestEvidence{Target: capability.Target{Team: owner.Capability.Team, RunID: c.run, NodeID: row.Ref.NodeID}})
	if err != nil {
		return err
	}
	principal, err := capability.ValidateAuthority(snapshot.Authorities, snapshot.Now)
	if err != nil {
		return err
	}
	binding := principal.Capability.Binding
	if principal.Authority != (capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: row.CapabilityPrefix}) || principal.Capability.Subject != owner.Capability.Subject || binding.Kind != capability.BindingNode || binding.RunID != c.run || binding.NodeID != row.Ref.NodeID || binding.Generation != row.Generation || binding.Holder != row.ClaimedBy {
		return localHostClientRefusal()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("local host client is closed")
	}
	c.nodes[row.Ref.NodeID] = localNodeBearer{raw: raw, prefix: row.CapabilityPrefix, generation: row.Generation, unit: principal.Capability.UnitID}
	return nil
}

func (c *LocalHostClient) nodeTransport(ctx context.Context, ref controllerwire.NodeRef) (*engineClient, localNodeBearer, error) {
	if c.node != "" {
		if ref.RunID != c.run || ref.NodeID != c.node {
			return nil, localNodeBearer{}, localHostClientRefusal()
		}
		return c.transport, localNodeBearer{}, c.live()
	}
	if ref.RunID != c.run || ref.NodeID == "" {
		return nil, localNodeBearer{}, localHostClientRefusal()
	}
	c.mu.Lock()
	award, ok := c.nodes[ref.NodeID]
	closed := c.closed
	c.mu.Unlock()
	if closed || !ok {
		return nil, localNodeBearer{}, localHostClientRefusal()
	}
	row, err := c.ReadNode(ctx, ref)
	if err != nil {
		return nil, localNodeBearer{}, err
	}
	if row.Generation != award.generation || row.CapabilityPrefix != award.prefix && (row.FinishedAt == nil || row.StateActor.CapabilityPrefix != award.prefix) {
		return nil, localNodeBearer{}, localHostClientRefusal()
	}
	transport := *c.transport
	transport.bearer = func(context.Context) (string, error) { return award.raw, c.live() }
	return &transport, award, nil
}

// RenewNode renews the node award independently of its hosting owner.
func (c *LocalHostClient) RenewNode(ctx context.Context, ref controllerwire.NodeRef, actor store.Actor, executing bool) (store.LeaseBeat, error) {
	transport, award, err := c.nodeTransport(ctx, ref)
	if err != nil {
		return store.LeaseBeat{}, err
	}
	if c.node != "" {
		principal, err := c.Principal(ctx)
		if err != nil {
			return store.LeaseBeat{}, err
		}
		award.prefix, award.generation, award.unit = principal.Capability.Prefix, principal.Capability.Binding.Generation, principal.Capability.UnitID
	}
	if award.unit == "" || actor.CapabilityPrefix != award.prefix || actor.Generation != award.generation {
		return store.LeaseBeat{}, localHostClientRefusal()
	}
	response, err := transport.Renew(ctx, award.unit, controllerwire.RenewalRequest{Executing: executing})
	return store.LeaseBeat{ExpiresAt: response.ExpiresAt, CancelRequested: response.CancelRequested, CancelReason: response.CancelReason, Watched: response.Watched, WatchedNodes: response.WatchedJobs}, err
}

// NodeController selects a retained current node bearer for execution reports and renewal.
func (c *LocalHostClient) NodeController(ctx context.Context, ref controllerwire.NodeRef) (controllerwire.Controller, error) {
	transport, _, err := c.nodeTransport(ctx, ref)
	if err != nil {
		return nil, err
	}
	return transport, nil
}

// Close releases retained node credentials and refuses subsequent operations.
func (c *LocalHostClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.nodes)
	c.closed = true
	return nil
}

func localHostClientRefusal() error {
	return &apierr.Error{Code: apierr.Forbidden, Message: "operation is outside the local host award"}
}

var (
	_ capability.AuthenticationReader = (*LocalHostClient)(nil)
	_ capability.BindingChecker       = (*LocalHostClient)(nil)
	_ interface {
		ReadSnapshot(context.Context, string) (store.RunSnapshot, error)
	} = (*LocalHostClient)(nil)
	_ interface {
		ReadNode(context.Context, controllerwire.NodeRef) (store.NodeRow, error)
	} = (*LocalHostClient)(nil)
	_ interface {
		TransitionRun(context.Context, store.RunTransition) (store.Applied, error)
	} = (*LocalHostClient)(nil)
	_ interface {
		TransitionNode(context.Context, store.NodeTransition) (store.Applied, error)
	} = (*LocalHostClient)(nil)
)

// AdoptCapacity checks continuously held capacity under the authenticated owner award.
func (c *LocalHostClient) AdoptCapacity(ctx context.Context, key, id string, binding store.CapacityBinding, _ store.Actor) (store.CapacityAdoption, error) {
	var result store.CapacityAdoption
	err := c.capacity(ctx, "/adopt", localhostwire.LocalCapacityRequest{Key: key, Binding: binding}, &result)
	return result, err
}

// AcquireCapacity admits pending work under the authenticated owner award.
func (c *LocalHostClient) AcquireCapacity(ctx context.Context, request store.AcquireSlotRequest, binding store.CapacityBinding, _ store.Actor) (store.AcquireSlotResponse, error) {
	var result store.AcquireSlotResponse
	err := c.capacity(ctx, "/acquire", localhostwire.LocalCapacityRequest{Key: request.Key, Binding: binding}, &result)
	return result, err
}

// ResolveCapacityWaiter uses the store's sealed queue policy and persisted arrival.
func (c *LocalHostClient) ResolveCapacityWaiter(ctx context.Context, key string, binding store.CapacityBinding, _ store.Actor) (store.WaiterResolution, error) {
	var result store.WaiterResolution
	err := c.capacity(ctx, "/resolve", localhostwire.LocalCapacityRequest{Key: key, Binding: binding}, &result)
	return result, err
}

func (c *LocalHostClient) capacity(ctx context.Context, operation string, body localhostwire.LocalCapacityRequest, result any) error {
	if c.node != "" || body.Binding.RunID != c.run {
		return localHostClientRefusal()
	}
	return c.request(ctx, "POST", "/capacity"+operation, "", body, result)
}
