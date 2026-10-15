package capability

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

type authenticator struct{ reader AuthenticationReader }

// NewAuthenticator verifies fresh store snapshots. No credential facts are cached, so
// every request observes ancestry and membership without a cache invalidation window.
func NewAuthenticator(reader AuthenticationReader) Authenticator {
	return &authenticator{reader: reader}
}

func (a *authenticator) Authenticate(ctx context.Context, presentation Presentation, route RoutePolicy) (Authenticated, error) {
	if a.reader == nil || validateRoutePolicy(route) != nil || route.Authentication != AuthCapability {
		return Authenticated{}, policyFailure()
	}
	lookup, err := credentialLookup(presentation)
	if err != nil {
		return Authenticated{}, err
	}
	evidence := presentation.Evidence
	evidence.Method, evidence.Pattern = route.Method, route.Path
	snapshot, err := a.reader.ReadAuthentication(ctx, lookup, evidence)
	if err != nil {
		return Authenticated{}, err
	}
	principal, err := ValidateAuthority(snapshot.Authorities, snapshot.Now)
	if err != nil {
		return Authenticated{}, err
	}
	if !matchesLookup(principal, lookup) {
		return Authenticated{}, unauthenticated()
	}
	auth := Authenticated{Principal: principal, Method: route.Method, Pattern: route.Path}
	for i, access := range route.Access {
		if live, ok := liveAccess(principal, snapshot.Binding, evidence, access.Liveness, snapshot.Now); ok {
			live.RuleIndex = i
			auth.Live = append(auth.Live, live)
		}
	}
	if len(auth.Live) == 0 {
		return Authenticated{}, unauthenticated()
	}
	return auth, nil
}

// ValidateAuthority checks complete ancestry and current membership. The store's
// transactional AuthorityResolver can use it for the same checks at mutation time.
// Unit liveness is separate because terminal result replay has a narrower exception.
func ValidateAuthority(chain []AuthorityState, now time.Time) (Principal, error) {
	if now.IsZero() {
		return Principal{}, policyFailure()
	}
	if len(chain) == 0 {
		return Principal{}, unauthenticated()
	}
	seen := make(map[AuthorityRef]bool, len(chain))
	for i, state := range chain {
		p, c := state.Principal, state.Principal.Capability
		local := p.Class == CredentialPeer || p.Class == CredentialAnonymous
		if !state.MembershipValid || !validIdentity(p) || !credentialAlive(c, now) ||
			local && (len(chain) != 1 || c.Parent != nil) || !local && seen[p.Authority] {
			return Principal{}, unauthenticated()
		}
		seen[p.Authority] = true
		if i+1 == len(chain) {
			if c.Parent != nil {
				return Principal{}, unauthenticated()
			}
		} else if c.Parent == nil || *c.Parent != chain[i+1].Principal.Authority {
			return Principal{}, unauthenticated()
		}
		if c.Purpose == PurposeHere && c.Issuance == Award {
			if i+1 == len(chain) || chain[i+1].Principal.Capability.Team != c.Team ||
				!slices.Contains(chain[i+1].Principal.Capability.Grants, RunsWrite) {
				return Principal{}, unauthenticated()
			}
		}
	}
	principal := chain[0].Principal
	for _, state := range chain {
		c := state.Principal.Capability
		for _, ceiling := range []*time.Time{c.ExpiresAt, c.RevokedAt, c.Binding.Deadline} {
			if ceiling != nil && (principal.Capability.ExpiresAt == nil || ceiling.Before(*principal.Capability.ExpiresAt)) {
				principal.Capability.ExpiresAt = ceiling
			}
		}
	}
	return principal, nil
}

func validIdentity(p Principal) bool {
	c := p.Capability
	if c.Audience != Controller && c.Audience != NodeHost {
		return false
	}
	if !slices.Contains([]SubjectKind{SubjectAccount, SubjectMachine, SubjectService, SubjectOperator, SubjectLocal, SubjectAnonymous}, c.Subject.Kind) {
		return false
	}
	if c.Subject.Kind != SubjectAnonymous && c.Subject.ID == "" {
		return false
	}
	switch p.Class {
	case CredentialBearer:
		return p.Authority.Kind == AuthorityCapability && p.Authority.ID != "" && c.Prefix == p.Authority.ID
	case CredentialSession:
		return p.Authority.Kind == AuthoritySession && p.Authority.ID != "" && c.Subject.Kind == SubjectAccount
	case CredentialConsole:
		return p.Authority.Kind == AuthoritySession && p.Authority.ID != "" && c.Subject.Kind == SubjectOperator
	case CredentialPeer:
		return c.Subject.Kind == SubjectLocal && c.Binding.Kind == BindingNone && c.Unit == ""
	case CredentialAnonymous:
		return c.Subject.Kind == SubjectAnonymous && c.Binding.Kind == BindingNone && c.Unit == ""
	}
	return false
}

func credentialAlive(c Capability, now time.Time) bool {
	return !c.IssuedAt.After(now) &&
		(c.ExpiresAt == nil || now.Before(*c.ExpiresAt)) &&
		(c.RevokedAt == nil || now.Before(*c.RevokedAt)) &&
		(c.Binding.Deadline == nil || now.Before(*c.Binding.Deadline))
}

func credentialLookup(p Presentation) (CredentialLookup, error) {
	key := CredentialLookup{Class: p.Class}
	switch p.Class {
	case CredentialBearer:
		var suffix string
		switch {
		case strings.HasPrefix(p.Raw, ControllerPrefix):
			key.Audience, suffix = Controller, strings.TrimPrefix(p.Raw, ControllerPrefix)
		case strings.HasPrefix(p.Raw, NodeHostPrefix):
			key.Audience, suffix = NodeHost, strings.TrimPrefix(p.Raw, NodeHostPrefix)
		default:
			return key, unauthenticated()
		}
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(suffix)
		if err != nil || len(decoded) != 32 || len(suffix) != 43 {
			return key, unauthenticated()
		}
		key.Digest = sha256.Sum256([]byte(p.Raw))
	case CredentialSession, CredentialConsole:
		key.Audience = Controller
		if p.Raw == "" || strings.TrimSpace(p.Raw) != p.Raw {
			return key, unauthenticated()
		}
		key.Digest = sha256.Sum256([]byte(p.Raw))
	case CredentialPeer:
		if p.Raw != "" || p.Peer.Kind != SubjectLocal || p.Peer.ID == "" {
			return key, unauthenticated()
		}
		key.Peer = p.Peer
	case CredentialAnonymous:
		if p.Raw != "" || p.Peer != (Subject{}) {
			return key, unauthenticated()
		}
	default:
		return key, unauthenticated()
	}
	return key, nil
}

func matchesLookup(p Principal, key CredentialLookup) bool {
	if p.Class != key.Class {
		return false
	}
	switch key.Class {
	case CredentialBearer:
		return p.Capability.Digest == key.Digest && p.Capability.Audience == key.Audience
	case CredentialSession, CredentialConsole:
		return p.Authority.ID == hex.EncodeToString(key.Digest[:])
	case CredentialPeer:
		return p.Capability.Subject == key.Peer
	case CredentialAnonymous:
		return p.Capability.Subject.Kind == SubjectAnonymous
	}
	return false
}

func liveAccess(p Principal, state BindingState, evidence RequestEvidence, class Liveness, now time.Time) (LiveAccess, bool) {
	c, b := p.Capability, p.Capability.Binding
	if c.Audience == NodeHost {
		return LiveAccess{}, state.ProcessAlive && !state.RunCancelled && !state.RunFinished && sameFence(b, state.Current)
	}
	if !isClaim(p) {
		return LiveAccess{}, c.Unit == "" && (b.Kind == BindingNone || b.Kind == BindingPush || b.Kind == BindingRun)
	}
	// safety: eval authority belongs to its own lease even during bootstrap source acquisition.
	if c.Unit == UnitEval || class == LiveExpansion {
		return LiveAccess{}, expansionLive(b, state, now)
	}
	if c.Purpose == PurposeHere && b.Generation == 0 {
		return LiveAccess{}, hereLive(b, state, class, now)
	}
	if !sameFence(b, state.Current) {
		return LiveAccess{}, false
	}
	active := state.FinishedAt == nil && !state.RunFinished && now.Before(state.LeaseExpiresAt)
	switch class {
	case LiveSensitive, LiveProcess:
		return LiveAccess{}, active && !state.RunCancelled
	case LiveCredential, LiveReporting, LiveHereClaim:
		return LiveAccess{}, active
	case LiveResult:
		if active {
			return LiveAccess{}, true
		}
		replay := state.FinishedAt != nil && resultMatches(p, state.Result, evidence)
		return LiveAccess{ResultReplay: replay}, replay
	case LiveLogTail:
		if active {
			return LiveAccess{}, true
		}
		tail := c.Unit == UnitNode && b.Attempt > 0 && state.FinishedAt != nil && !state.LogSealed &&
			!now.Before(*state.FinishedAt) && now.Before(state.FinishedAt.Add(limits.LogSealGrace)) && logTarget(c, evidence.Target)
		return LiveAccess{LogTailAttempt: b.Attempt}, tail
	}
	return LiveAccess{}, false
}

func sameFence(a, b Binding) bool {
	return a.Kind == b.Kind && a.RunID != "" && a.RunID == b.RunID && a.NodeID == b.NodeID &&
		a.Generation > 0 && a.Generation == b.Generation && a.Holder != "" && a.Holder == b.Holder && a.Attempt == b.Attempt
}

func hereLive(b Binding, state BindingState, class Liveness, now time.Time) bool {
	if state.RunFinished || b.RunID == "" || b.RunID != state.Current.RunID ||
		b.Reservation != "here:"+b.RunID || b.Reservation != state.Current.Reservation {
		return false
	}
	if class == LiveHereClaim || class == LiveCredential {
		return !state.RunCancelled && (state.HereClaimed || b.ClaimBy != nil && now.Before(*b.ClaimBy))
	}
	if class == LiveSensitive || class == LiveProcess {
		if state.RunCancelled {
			return false
		}
	}
	return state.HereClaimed && state.FinishedAt == nil && now.Before(state.LeaseExpiresAt)
}

func expansionLive(b Binding, state BindingState, now time.Time) bool {
	expansion := state.Expansion
	if b.Expansion == nil || expansion == nil || expansion.Lease != *b.Expansion ||
		expansion.Lease.ID == "" || expansion.Lease.Holder == "" || expansion.Lease.Generation <= 0 ||
		state.RunCancelled || state.RunFinished || state.FinishedAt == nil ||
		b.RunID == "" || b.RunID != state.Current.RunID || b.NodeID == "" || b.NodeID != state.Current.NodeID ||
		b.Generation <= 0 || b.Generation != state.Current.Generation || !now.Before(expansion.ExpiresAt) {
		return false
	}
	if expansion.Lease.Kind == UnitEval {
		return expansion.ControllerLeaseExpiresAt != nil && now.Before(*expansion.ControllerLeaseExpiresAt)
	}
	return expansion.Lease.Kind == UnitTrigger
}

func resultMatches(p Principal, record *RecordedResult, evidence RequestEvidence) bool {
	if record == nil || len(evidence.ResultDigest) != 64 {
		return false
	}
	if _, err := hex.DecodeString(evidence.ResultDigest); err != nil {
		return false
	}
	b, target := p.Capability.Binding, evidence.Target
	return record.Actor == p.Authority && record.Method == evidence.Method && record.Pattern == evidence.Pattern &&
		record.RunID == target.RunID && record.NodeID == target.NodeID && record.ClaimID == target.ClaimID &&
		record.Generation == b.Generation && record.Attempt == b.Attempt && record.Digest == evidence.ResultDigest
}

func logTarget(c Capability, target Target) bool {
	if target.RunID != c.Binding.RunID {
		return false
	}
	if target.ClaimID != "" {
		return target.ClaimID == c.UnitID
	}
	return target.NodeID == c.Binding.NodeID
}
