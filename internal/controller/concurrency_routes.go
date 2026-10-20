package controller

import (
	"context"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// ConcurrencyStore rechecks authority, declared groups, fences and reference edges in each transaction.
type ConcurrencyStore interface {
	Team() store.Team
	AcquireAuthorizedSlot(context.Context, capability.Authorized, store.AcquireSlotRequest) (store.AcquireSlotResponse, error)
	HeartbeatAuthorizedSlot(context.Context, capability.Authorized, string, string, time.Duration) (time.Time, bool, error)
	ReadAuthorizedHolder(context.Context, capability.Authorized, string, string) (store.ConcurrencyHolder, error)
	ReleaseAuthorizedSlot(context.Context, capability.Authorized, string, string, string, string, string, time.Duration) error
	ReadAuthorizedGroup(context.Context, capability.Authorized, string) (*store.ConcurrencyState, error)
	ResolveAuthorizedWaiter(context.Context, capability.Authorized, string, string, string, string, string, string, bool) (store.WaiterResolution, error)
	CancelAuthorizedWaiter(context.Context, capability.Authorized, string, controllerwire.NodeRef) (bool, error)
	ForceReleaseAuthorizedSlots(context.Context, capability.Authorized, string) ([]store.ConcurrencyHolder, error)
}

func (s *Server) concurrencyStore(ctx context.Context, team string) (ConcurrencyStore, error) {
	return tenantPort[ConcurrencyStore](ctx, s, store.Team(team))
}

func (s *Server) concurrencyEndpoints() []Endpoint {
	return []Endpoint{
		{Method: "GET", Pattern: "/api/v1/queue/groups/{group}", Authentication: capability.AuthCapability, Decode: concurrencyDecoder(""), ResolveTarget: resolveConcurrencyTarget, Handle: s.handleConcurrencyState},
		{Method: "POST", Pattern: "/api/v1/queue/groups/{group}/acquire", Authentication: capability.AuthCapability, Decode: concurrencyDecoder("/acquire"), ResolveTarget: resolveConcurrencyTarget, Handle: s.handleAcquireSlot},
		{Method: "POST", Pattern: "/api/v1/queue/groups/{group}/heartbeat", Authentication: capability.AuthCapability, Decode: concurrencyDecoder("/heartbeat"), ResolveTarget: resolveConcurrencyTarget, Handle: s.handleHeartbeatSlot},
		{Method: "POST", Pattern: "/api/v1/queue/groups/{group}/release", Authentication: capability.AuthCapability, Decode: concurrencyDecoder("/release"), ResolveTarget: resolveConcurrencyTarget, Handle: s.handleReleaseSlot},
		{Method: "POST", Pattern: "/api/v1/queue/groups/{group}/cancel-waiter", Authentication: capability.AuthCapability, Decode: concurrencyDecoder("/cancel-waiter"), ResolveTarget: resolveConcurrencyTarget, Handle: s.handleCancelWaiter},
		{Method: "GET", Pattern: "/api/v1/queue/groups/{group}/holder", Authentication: capability.AuthCapability, Decode: concurrencyDecoder("/holder"), ResolveTarget: resolveConcurrencyTarget, Handle: s.handleObserveSlot},
		{Method: "GET", Pattern: "/api/v1/queue/groups/{group}/resolve", Authentication: capability.AuthCapability, Decode: concurrencyDecoder("/resolve"), ResolveTarget: resolveConcurrencyTarget, Handle: s.handleResolveWaiter},
		{Method: "POST", Pattern: "/api/v1/queue/groups/{group}/force-release", Authentication: capability.AuthCapability, Decode: concurrencyDecoder("/force-release"), ResolveTarget: resolveConcurrencyTarget, Handle: s.handleForceRelease},
	}
}

func concurrencyDecoder(operation string) func(*http.Request) (capability.RequestEvidence, error) {
	return func(r *http.Request) (capability.RequestEvidence, error) { return concurrencyTarget(r, operation) }
}

func resolveConcurrencyTarget(_ context.Context, p capability.Principal, target capability.Target) (capability.Target, error) {
	if target.SlotRunID == "" {
		target.SlotRunID, target.SlotNodeID = p.Capability.Binding.RunID, p.Capability.Binding.NodeID
	}
	target.SlotGeneration = p.Capability.Binding.Generation
	return target, nil
}

func concurrencyTarget(r *http.Request, operation string) (capability.RequestEvidence, error) {
	e := capability.RequestEvidence{Target: capability.Target{SlotKey: r.PathValue("group")}}
	if err := controllerwire.ValidateQueueGroupName(e.Target.SlotKey); err != nil {
		return e, invalidEngineBody("invalid concurrency group")
	}
	switch operation {
	case "/acquire":
		var body acquireSlotReq
		if err := decodeConcurrencyRequest(r, &body); err != nil {
			return e, err
		}
		if body.RunID == "" || body.HolderID == "" {
			return e, invalidEngineBody("holder_id and run_id are required")
		}
		e.Target.SlotRunID, e.Target.SlotNodeID, e.Target.SlotHolderID = body.RunID, body.NodeID, body.HolderID
	case "/heartbeat":
		var body heartbeatSlotReq
		if err := decodeConcurrencyRequest(r, &body); err != nil {
			return e, err
		}
		e.Target.SlotHolderID = body.HolderID
	case "/release":
		var body releaseSlotReq
		if err := decodeConcurrencyRequest(r, &body); err != nil {
			return e, err
		}
		e.Target.SlotHolderID = body.HolderID
	case "/cancel-waiter":
		var body cancelWaiterReq
		if err := decodeConcurrencyRequest(r, &body); err != nil {
			return e, err
		}
		if body.RunID == "" {
			return e, invalidEngineBody("run_id is required")
		}
		e.Target.SlotRunID, e.Target.SlotNodeID = body.RunID, body.NodeID
	case "/holder", "/resolve":
		q := r.URL.Query()
		for name, values := range q {
			if len(values) != 1 || name == "node_id" || name == "leader_node_id" {
				return e, invalidEngineBody("invalid concurrency selector")
			}
		}
		if operation == "/holder" {
			e.Target.SlotHolderID = q.Get("holder_id")
		} else {
			e.Target.SlotRunID, e.Target.SlotNodeID = q.Get("run_id"), q.Get("job_id")
			if e.Target.SlotRunID == "" || q.Get("bypass_read") != "" && q.Get("bypass_read") != "true" && q.Get("bypass_read") != "false" {
				return e, invalidEngineBody("invalid waiter selector")
			}
		}
	}
	if operation == "/heartbeat" || operation == "/release" || operation == "/holder" {
		if strings.TrimSpace(e.Target.SlotHolderID) == "" {
			return e, invalidEngineBody("holder_id is required")
		}
	}
	return e, nil
}

func decodeConcurrencyRequest(r *http.Request, body any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxJSONBody)
	if err := decodeEngineRequest(r, body); err != nil {
		return err
	}
	var seconds int
	var invalid bool
	switch b := body.(type) {
	case *acquireSlotReq:
		seconds = b.LeaseSecs
		invalid = b.Max < 0 || b.Cost < 0 || b.CacheTTLNS < 0 || b.CancelTimeoutNS < 0
	case *heartbeatSlotReq:
		seconds = b.LeaseSecs
	case *releaseSlotReq:
		invalid = b.CacheTTLNS < 0
	}
	if invalid || seconds < 0 || int64(seconds) > math.MaxInt64/int64(time.Second) {
		return invalidEngineBody("invalid concurrency duration or capacity")
	}
	return nil
}
