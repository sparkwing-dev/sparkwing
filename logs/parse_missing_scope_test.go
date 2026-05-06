package logs

import "testing"

// IMP-022: parseMissingScope is the chokepoint for AuthError.Scope.
// Pin all three input shapes here so a future tweak to either the
// JSON parsing or the string-match fallback gets caught immediately.
func TestParseMissingScope(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "json with missing_scope",
			body: `{"error":"missing_scope","missing_scope":"logs.write","message":"token lacks required scope: logs.write"}`,
			want: "logs.write",
		},
		{
			name: "json with missing_scope and reworded message",
			body: `{"error":"missing_scope","missing_scope":"runs.write","message":"completely different phrasing"}`,
			want: "runs.write",
		},
		{
			name: "json without missing_scope falls through to string match (none here)",
			body: `{"error":"forbidden","message":"denied by policy"}`,
			want: "",
		},
		{
			name: "plain string fallback (pre-IMP-022 server)",
			body: "token lacks required scope: logs.write",
			want: "logs.write",
		},
		{
			name: "plain string with trailing punctuation",
			body: "token lacks required scope: logs.write.",
			want: "logs.write",
		},
		{
			name: "neither shape -- empty",
			body: "boom",
			want: "",
		},
		{
			name: "empty body",
			body: "",
			want: "",
		},
		{
			name: "non-json body with marker text -- string fallback recovers",
			body: "rejected: token lacks required scope: nodes.claim (try with admin)",
			want: "nodes.claim",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseMissingScope(tc.body)
			if got != tc.want {
				t.Errorf("parseMissingScope(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}
