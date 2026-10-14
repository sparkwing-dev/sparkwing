package nodeproto

// safety: ErrorUse documents consumers of C2 codes; it defines neither error types nor HTTP status policy.
type ErrorUse struct {
	Code    string
	Surface string
}

// safety: ErrorUses is the complete SDK/host/node-route dependency inventory for C2's canonical registry.
// These strings must resolve in apierr; this table must not become a parallel validation registry.
func ErrorUses() []ErrorUse {
	return []ErrorUse{
		{"unknown_op", "sdk"},
		{"unknown_pipeline", "sdk"},
		{"unknown_job", "sdk,node_route"},
		{"unknown_step", "sdk"},
		{"unknown_closure", "sdk"},
		{"no_plan", "sdk"},
		{"bad_request", "sdk,node_route"},
		{"invalid_args", "sdk"},
		{"plan_failed", "sdk"},
		{"plan_drift", "host"},
		{"plan_invalid", "host"},
		{"protocol_mismatch", "hello"},
		{"protocol_error", "host"},
		{"sdk_exited", "host"},
		{"frames_closed", "host"},
		{"sdk_error", "host"},
		{"unauthenticated", "node_route"},
		{"forbidden", "node_route"},
		{"lock_held", "host,node_route"},
		{"illegal_transition", "host,node_route"},
		{"spec_drift", "host"},
		{"secret_undeclared", "node_route"},
		{"secret_not_found", "node_route"},
		{"secret_unavailable", "node_route"},
		{"not_ready", "node_route"},
		{"no_output", "node_route"},
		{"not_dependency", "node_route"},
		{"no_run", "node_route"},
		{"reference_undeclared", "node_route"},
		{"upstream_unavailable", "node_route"},
		{"too_many_requests", "node_route"},
		{"cycle", "node_route"},
		{"child_repo", "node_route"},
		{"unknown_child", "node_route"},
		{"oidc_unavailable", "node_route"},
	}
}
