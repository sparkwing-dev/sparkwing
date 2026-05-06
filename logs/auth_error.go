package logs

// AuthErrorBody is the on-wire JSON shape returned for 401/403
// responses by the logs service (and matched by other sparkwing
// auth surfaces — pkg/controller/auth.go, internal/local/auth.go).
//
// Pinning a structured shape decouples client-side scope parsing
// from the human-readable message phrasing. Pre-IMP-022 callers
// (older controllers, non-controller proxies) emit a plain-text
// body; the client falls back to a string match on Message.
//
// Empty fields omit themselves so 401 bodies (no scope/principal
// resolved) stay compact.
type AuthErrorBody struct {
	// Error is a stable code: "unauthenticated" for 401,
	// "missing_scope" for 403. Programmatic clients branch on this.
	Error string `json:"error"`

	// MissingScope names the scope the request lacked. Set on 403
	// only; empty otherwise.
	MissingScope string `json:"missing_scope,omitempty"`

	// Principal is the authenticated identity in "kind:name" form
	// (e.g. "runner:warm-runner-7"). Empty when auth never resolved.
	Principal string `json:"principal,omitempty"`

	// Message is the human-readable string. Stays compatible with
	// the pre-IMP-022 plain-text body so log readers don't degrade.
	Message string `json:"message"`
}
