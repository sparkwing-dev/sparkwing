package blob

import (
	"fmt"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// ScopeFor is the scope a run's records are written under: the verified ref for a
// signed App delivery, and the writer's own scope for every other trust class.
func ScopeFor(p runspec.Provenance) RefScope {
	if p.Verified() {
		return RefScope{Ref: p.Ref(), Verified: true}
	}
	return RefScope{Ref: p.Ref(), Writer: string(p.Writer())}
}

// CapabilityScope renders the scope as sealed data grants name it: the bare ref when
// verified, and manual:<writer>:<ref> otherwise. A writer never holds a colon.
func (r RefScope) CapabilityScope() string {
	if r.Verified {
		return r.Ref
	}
	return "manual:" + r.Writer + ":" + r.Ref
}

// ParseCapabilityScope reverses CapabilityScope.
func ParseCapabilityScope(scope string) RefScope {
	rest, manual := strings.CutPrefix(scope, "manual:")
	if !manual {
		return RefScope{Ref: scope, Verified: true}
	}
	writer, ref, _ := strings.Cut(rest, ":")
	return RefScope{Ref: ref, Writer: writer}
}

func validAttempt(a runspec.AttemptIdentity) bool {
	return a.Team != "" && a.RunID != "" && a.NodeID != "" && a.Attempt > 0 && a.Generation > 0
}

func validUpload(i UploadIdentity) bool {
	if i.Team == "" || i.RunID == "" || i.Principal == "" || i.CapabilityRef == "" {
		return false
	}
	if i.Attempt.Set && (!validAttempt(i.Attempt.Value) || i.Attempt.Value.Team != i.Team || i.Attempt.Value.RunID != i.RunID) {
		return false
	}
	switch i.Kind {
	case UploadJob:
		return i.Attempt.Set && !i.Unit.Set
	case UploadPublication:
		return !i.Unit.Set
	case UploadOwnerUnit:
		if i.Attempt.Set || !i.Unit.Set || i.Unit.Value.ID == "" || i.Unit.Value.Generation <= 0 {
			return false
		}
		u := i.Unit.Value
		switch u.Kind {
		case UploadTriggerUnit, UploadPlanUnit:
			return !u.Source.Set
		case UploadEvalUnit:
			return u.Source.Set && validAttempt(u.Source.Value) && u.Source.Value.Team == i.Team && u.Source.Value.RunID == i.RunID
		}
	}
	return false
}

// AuthorizeUpload checks the upload kind against its bound identity.
func AuthorizeUpload(a Authority, i UploadIdentity, kind Kind) error {
	if !validUpload(i) || a.Principal != i.Principal || a.CapabilityRef != i.CapabilityRef {
		return ErrDigestNotReadable
	}
	if a.Attempt.Set != i.Attempt.Set || (a.Attempt.Set && a.Attempt.Value != i.Attempt.Value) {
		return ErrDigestNotReadable
	}
	// safety: a matching principal alone cannot transfer an owner or publication fence.
	if (a.Upload.Set && a.Upload.Value != i) || (i.Kind != UploadJob && !a.Upload.Set) {
		return ErrDigestNotReadable
	}
	for _, rule := range UploadRules {
		if rule.IdentityKind == i.Kind && rule.RequiresAttempt == i.Attempt.Set && rule.RequiresUnit == i.Unit.Set && slices.Contains(rule.RecordKinds, kind) {
			return nil
		}
	}
	return ErrDigestNotReadable
}

// AuthorizeEdge checks receipt identity or a declared readable edge.
func AuthorizeEdge(r EdgeRequest) error {
	if !validDigest(r.Digest) || !slices.Contains(Kinds, r.TargetKind) {
		return ErrDigestNotReadable
	}
	if r.OwnReceipt.Set && r.OwnReceipt.Value.Digest == r.Digest && AuthorizeUpload(r.Authority, r.OwnReceipt.Value.Identity, r.TargetKind) == nil {
		return nil
	}
	a := r.Authority
	if a.Principal == "" || a.CapabilityRef == "" {
		return ErrDigestNotReadable
	}
	if a.Attempt.Set {
		if !validAttempt(a.Attempt.Value) || r.ReadSet.Reader != a.Attempt.Value {
			return ErrDigestNotReadable
		}
	} else if a.Upload.Set && (!validUpload(a.Upload.Value) || a.Upload.Value.Kind != UploadOwnerUnit || a.Upload.Value.Principal != a.Principal || a.Upload.Value.CapabilityRef != a.CapabilityRef) || a.Repo == "" || r.ReadSet.Reader != (runspec.AttemptIdentity{}) {
		return ErrDigestNotReadable
	}
	for _, edge := range r.ReadSet.Edges {
		if edge.Reader != r.ReadSet.Reader || edge.Digest != r.Digest || !slices.Contains(edgeKinds, edge.Kind) {
			continue
		}
		if a.Attempt.Set {
			return nil
		}
		key := edge.Record
		if a.Upload.Set && edge.Kind == EdgeSealedSource && r.TargetKind == KindSourceBundle && key.Kind == KindSourceBundle && key.Team == a.Upload.Value.Team && key.Scope.Repo == a.Repo {
			return nil
		}
		if a.Upload.Set && a.Upload.Value.Kind == UploadOwnerUnit && edge.Kind == EdgeDependency && (key.Kind == KindOutput || key.Kind == KindManifest) && key.Team == a.Upload.Value.Team && key.Scope.Repo == a.Repo && cacheReadKind(r.TargetKind) {
			return nil
		}
		if edge.Kind == EdgeMemoScope && cacheReadKind(r.TargetKind) && cacheReadKind(key.Kind) && key.Team != "" && key.Scope.Repo == a.Repo && key.Scope.Ref.Verified {
			return nil
		}
	}
	return ErrDigestNotReadable
}

func cacheReadKind(kind Kind) bool {
	return kind == KindOutput || kind == KindManifest || kind == KindMemo || kind == KindDepcache
}

// CheckBinaryExecution requires trusted provenance for executable bytes.
func CheckBinaryExecution(e BinaryExecution) error {
	if !validDigest(e.Digest) || e.Digest != e.Record.Digest {
		return ErrIntegrity
	}
	if e.Trust.Team == "" || e.Trust.Team != e.Record.Key.Team || e.Record.Key.Kind != KindBinary {
		return ErrDigestNotReadable
	}
	switch e.Record.Provenance {
	case ProvenanceCloud:
		return nil
	case ProvenanceLocal:
		if e.Trust.TrustLocalBuilds {
			return nil
		}
	}
	return fmt.Errorf("%w: binary provenance requires trust_local_builds", ErrDigestNotReadable)
}
