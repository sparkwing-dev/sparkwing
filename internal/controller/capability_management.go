package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// CapabilityManager rechecks authority and ownership in scoped inventory and rotation transactions.
type CapabilityManager interface {
	Team() store.Team
	ListCapabilities(context.Context, capability.Authorized, capability.InventoryFilter) ([]capability.Metadata, error)
	ReadCapability(context.Context, capability.Authorized) (capability.Metadata, error)
	RotateCapability(context.Context, capability.Authorized, time.Duration, *time.Time) (capability.Rotation, error)
}

type capabilityRotationRequest struct {
	GraceSeconds json.RawMessage `json:"grace_secs"`
	ExpiresAt    json.RawMessage `json:"expires_at"`
}

type capabilityRotationResponse struct {
	Capability   string              `json:"capability"`
	New          capability.Metadata `json:"new"`
	OldPrefix    string              `json:"old_prefix"`
	OldRevokedAt time.Time           `json:"old_revoked_at"`
}

func (s *Server) capabilityManager(ctx context.Context, team string) (CapabilityManager, error) {
	if team == "" {
		if s.operatorCapabilities == nil {
			return nil, routepolicy.MissingEndpointPolicy()
		}
		return s.operatorCapabilities, nil
	}
	return tenantPort[CapabilityManager](ctx, s, store.Team(team))
}

func (s *Server) capabilityEndpoints() []controllerwire.Endpoint {
	e := s.engineController
	return []controllerwire.Endpoint{
		{Method: "GET", Pattern: "/api/v1/overflow", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleReadOverflow},
		{Method: "PUT", Pattern: "/api/v1/overflow", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleSetOverflow},
		{Method: "POST", Pattern: "/api/v1/runners", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleEnrollRunner},
		{Method: "GET", Pattern: "/api/v1/tokens", Authentication: capability.AuthCapability, Decode: capabilityListTarget, Handle: s.handleListCapabilities},
		{Method: "POST", Pattern: "/api/v1/tokens", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleIssueCredential},
		{Method: "GET", Pattern: "/api/v1/tokens/{token}", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleReadCapability},
		{Method: "DELETE", Pattern: "/api/v1/tokens/{token}", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: e.handleRevokeAuthority},
		{Method: "POST", Pattern: "/api/v1/tokens/{token}/rotate", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleRotateCapability},
		{Method: "GET", Pattern: "/api/v1/operator/tokens", Authentication: capability.AuthCapability, Decode: capabilityListTarget, Handle: s.handleListCapabilities},
		{Method: "POST", Pattern: "/api/v1/operator/tokens", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleIssueServiceCredential},
		{Method: "GET", Pattern: "/api/v1/operator/tokens/{token}", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleReadCapability},
		{Method: "DELETE", Pattern: "/api/v1/operator/tokens/{token}", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: e.handleRevokeAuthority},
		{Method: "POST", Pattern: "/api/v1/operator/tokens/{token}/rotate", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleRotateCapability},
	}
}

func capabilityListTarget(r *http.Request) (capability.RequestEvidence, error) {
	_, err := capabilityFilter(r.URL.RawQuery)
	return capability.RequestEvidence{}, err
}

func capabilityFilter(query string) (capability.InventoryFilter, error) {
	values, err := url.ParseQuery(query)
	if err != nil || len(values["kind"]) > 1 || len(values["include_revoked"]) > 1 {
		return capability.InventoryFilter{}, invalidEngineBody("invalid credential filter")
	}
	kind := capability.SubjectKind(values.Get("kind"))
	if _, present := values["kind"]; present && !slices.Contains([]capability.SubjectKind{capability.SubjectAccount, capability.SubjectMachine, capability.SubjectService, capability.SubjectOperator}, kind) {
		return capability.InventoryFilter{}, invalidEngineBody("kind must be account, machine, service, or operator")
	}
	revoked := values.Get("include_revoked")
	if _, present := values["include_revoked"]; present && revoked != "0" && revoked != "1" {
		return capability.InventoryFilter{}, invalidEngineBody("include_revoked must be 0 or 1")
	}
	return capability.InventoryFilter{SubjectKind: kind, IncludeRevoked: revoked == "1"}, nil
}

func (s *Server) handleListCapabilities(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	filter, err := capabilityFilter(r.URL.RawQuery)
	if err != nil {
		return err
	}
	manager, err := s.capabilityManager(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	rows, err := manager.ListCapabilities(r.Context(), a, filter)
	if err != nil {
		return engineLookupError(err)
	}
	out := make([]capability.Metadata, 0, len(rows))
	for _, row := range rows {
		if !validCapabilityMetadata(a, row) || filter.SubjectKind != "" && row.Subject.Kind != filter.SubjectKind {
			return errors.New("invalid credential inventory")
		}
		row.Grants = append([]capability.Grant{}, row.Grants...)
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
	return nil
}

func (s *Server) handleReadCapability(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	manager, err := s.capabilityManager(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	row, err := manager.ReadCapability(r.Context(), a)
	if err != nil {
		return engineLookupError(err)
	}
	if row.Prefix != a.Target.TokenPrefix || !validCapabilityMetadata(a, row) {
		return errors.New("invalid credential metadata")
	}
	noteAuditTarget(r.Context(), "token_prefix", row.Prefix)
	row.Grants = append([]capability.Grant{}, row.Grants...)
	writeJSON(w, http.StatusOK, row)
	return nil
}

func validCapabilityMetadata(a capability.Authorized, row capability.Metadata) bool {
	if row.Prefix == "" || row.Subject.ID == "" || row.Team != a.Target.Team || row.Audience != capability.Controller || row.IssuedAt.IsZero() {
		return false
	}
	if row.Team == "" {
		return row.Subject.Kind == capability.SubjectService || row.Subject.Kind == capability.SubjectOperator
	}
	if row.Subject.Kind != capability.SubjectAccount && row.Subject.Kind != capability.SubjectMachine {
		return false
	}
	return capability.OwnsToken(a.Principal, row)
}

func (s *Server) handleRotateCapability(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	grace, expires, err := decodeCapabilityRotation(r)
	if err != nil {
		return err
	}
	manager, err := s.capabilityManager(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	rotation, err := manager.RotateCapability(r.Context(), a, grace, expires)
	if err != nil {
		return engineLookupError(err)
	}
	if !validCapabilityRotation(a, rotation) {
		return errors.New("invalid credential rotation")
	}
	noteAuditTarget(r.Context(), "token_prefix", rotation.Previous.Prefix)
	c := rotation.Issued.Capability
	writeJSON(w, http.StatusOK, capabilityRotationResponse{
		Capability: rotation.Issued.Raw, OldPrefix: rotation.Previous.Prefix, OldRevokedAt: rotation.RevokesAt,
		New: capability.Metadata{
			Prefix: c.Prefix, Team: c.Team, Subject: c.Subject, Audience: c.Audience,
			Grants: append([]capability.Grant{}, c.Grants...), CreatedBy: rotation.Previous.CreatedBy, IssuedAt: c.IssuedAt,
			ExpiresAt: c.ExpiresAt, Metered: c.RootMetered,
		},
	})
	return nil
}

func decodeCapabilityRotation(r *http.Request) (time.Duration, *time.Time, error) {
	var request capabilityRotationRequest
	if err := decodeEngineRequest(r, &request); err != nil {
		return 0, nil, err
	}
	grace := defaultRotateGrace
	if request.GraceSeconds != nil {
		var seconds *int64
		if err := json.Unmarshal(request.GraceSeconds, &seconds); err != nil || seconds == nil || *seconds < 0 || *seconds > int64(maxRotateGrace/time.Second) {
			return 0, nil, invalidEngineBody("grace_secs must be between 0 and 604800")
		}
		grace = time.Duration(*seconds) * time.Second
	}
	var expires *time.Time
	if request.ExpiresAt != nil {
		if err := json.Unmarshal(request.ExpiresAt, &expires); err != nil || expires == nil || expires.IsZero() {
			return 0, nil, invalidEngineBody("expires_at must be a timestamp")
		}
	}
	return grace, expires, nil
}

func validCapabilityRotation(a capability.Authorized, r capability.Rotation) bool {
	old, next := r.Previous, r.Issued.Capability
	if !validCapabilityMetadata(a, old) || old.Prefix != a.Target.TokenPrefix || r.Issued.Raw == "" || r.RevokesAt.IsZero() ||
		next.Prefix == "" || next.Prefix == old.Prefix || next.Team != old.Team || next.Subject != old.Subject || next.Audience != old.Audience || next.RootMetered != old.Metered ||
		next.Binding != (capability.Binding{Kind: capability.BindingNone}) || next.Unit != "" || next.Issuance != capability.Delegate || next.RevokedAt != nil || next.IssuedAt.IsZero() {
		return false
	}
	for _, grant := range next.Grants {
		if !slices.Contains(old.Grants, grant) {
			return false
		}
	}
	return !r.RevokesAt.Before(next.IssuedAt) && (next.ExpiresAt == nil || next.ExpiresAt.After(next.IssuedAt))
}

var (
	_ CapabilityManager = (*store.Tenant)(nil)
	_ CapabilityManager = (*store.Operator)(nil)
)

// WithOperatorCapabilityManager supplies deployment-wide inventory and rotation transactions.
func (s *Server) WithOperatorCapabilityManager(manager CapabilityManager) *Server {
	s.operatorCapabilities = manager
	return s
}
