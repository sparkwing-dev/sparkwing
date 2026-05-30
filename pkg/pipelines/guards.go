package pipelines

import (
	"fmt"
	"strings"
)

// GuardToken is one parsed guard expression, ready to evaluate
// against a [GuardContext] at run start. Returned by ParseGuardToken;
// produced once per token at config-load time so dispatch-time
// evaluation stays allocation-free.
type GuardToken struct {
	Raw  string // original token string (for error messages)
	Kind GuardKind
	Arg  string // for KindProfileName / KindArg the left-hand side
	Val  string // for KindArg the right-hand side; for KindProfileName the profile name
}

// GuardKind is the parsed predicate variant.
type GuardKind int

const (
	KindUnknown GuardKind = iota
	KindProfileLocal
	KindProfileController
	KindProfileName    // profile-name:<name>
	KindGitBranch      // git-branch:<name>
	KindGitBranchOnDef // git-branch:default
	KindArg            // arg:<flag>=<value>
)

// GuardContext is everything a guard token needs to evaluate: the
// active profile shape + the resolved args by CLI flag name.
//
// The evaluator never reaches back into orchestrator or profile
// packages -- callers pack what they have into this context and
// hand it off. Keeps guards/predicates testable in isolation.
type GuardContext struct {
	// ProfileName is the active profile's name. Empty when no
	// profile chain is in scope (e.g. an untracked dispatch).
	ProfileName string

	// ProfileIsLocal is true when the active profile routes
	// in-process (no controller URL).
	ProfileIsLocal bool

	// Args is the resolved args map keyed by CLI flag name.
	// Values are stringified for comparison (the guard vocabulary
	// is `arg:flag=value` where value is a literal). Nil-safe.
	Args map[string]string

	// GitBranch is the branch the dispatch originated on. Empty
	// when no git context is in scope (cluster-side replays, etc.);
	// git-branch tokens never match an empty branch so guards stay
	// safe by default.
	GitBranch string

	// GitDefaultBranch is the repo's default branch (typically main
	// or master). Used by the git-branch:default token. Empty when
	// no git context is in scope.
	GitDefaultBranch string
}

// ParseGuardToken parses one flat token string into a GuardToken
// ready for evaluation. Returns an error naming the unknown form
// when the token doesn't match a known shape.
//
// Vocabulary (case-sensitive, kebab-case):
//
//	profile-local            -- matches when the active profile has no controller
//	profile-controller       -- matches when the active profile has a controller URL
//	profile-name:<name>      -- matches when the active profile's name equals <name>
//	git-branch:default       -- matches when dispatch is on the repo's default branch
//	git-branch:<name>        -- matches when dispatch is on the named branch
//	arg:<flag>=<value>       -- matches when the resolved arg equals the value
func ParseGuardToken(raw string) (GuardToken, error) {
	tok := GuardToken{Raw: raw}
	switch {
	case raw == "profile-local":
		tok.Kind = KindProfileLocal
	case raw == "profile-controller":
		tok.Kind = KindProfileController
	case strings.HasPrefix(raw, "profile-name:"):
		name := strings.TrimPrefix(raw, "profile-name:")
		if name == "" {
			return tok, fmt.Errorf("guard %q: profile-name: requires a non-empty name", raw)
		}
		tok.Kind = KindProfileName
		tok.Arg = name
	case raw == "git-branch:default":
		tok.Kind = KindGitBranchOnDef
	case strings.HasPrefix(raw, "git-branch:"):
		name := strings.TrimPrefix(raw, "git-branch:")
		if name == "" {
			return tok, fmt.Errorf("guard %q: git-branch: requires a non-empty name (or :default)", raw)
		}
		tok.Kind = KindGitBranch
		tok.Arg = name
	case strings.HasPrefix(raw, "arg:"):
		rest := strings.TrimPrefix(raw, "arg:")
		eq := strings.IndexByte(rest, '=')
		if eq <= 0 || eq == len(rest)-1 {
			return tok, fmt.Errorf("guard %q: arg: requires <flag>=<value>", raw)
		}
		tok.Kind = KindArg
		tok.Arg = rest[:eq]
		tok.Val = rest[eq+1:]
	default:
		return tok, fmt.Errorf("guard %q: unknown token; vocab: profile-local, profile-controller, profile-name:<n>, git-branch:default, git-branch:<n>, arg:<f>=<v>", raw)
	}
	return tok, nil
}

// Matches reports whether this token's predicate holds against ctx.
func (t GuardToken) Matches(ctx GuardContext) bool {
	switch t.Kind {
	case KindProfileLocal:
		return ctx.ProfileIsLocal
	case KindProfileController:
		return !ctx.ProfileIsLocal
	case KindProfileName:
		return ctx.ProfileName == t.Arg
	case KindGitBranch:
		return ctx.GitBranch != "" && ctx.GitBranch == t.Arg
	case KindGitBranchOnDef:
		return ctx.GitBranch != "" && ctx.GitDefaultBranch != "" && ctx.GitBranch == ctx.GitDefaultBranch
	case KindArg:
		return ctx.Args[t.Arg] == t.Val
	}
	return false
}

// Validate parses every token in Require and Reject, returning the
// first parse failure. Called from Config.Validate at load time so
// invalid guard syntax surfaces at parse rather than dispatch.
func (g Guards) Validate(pipeline string) error {
	for _, raw := range g.Require {
		if _, err := ParseGuardToken(raw); err != nil {
			return fmt.Errorf("pipeline %q: guards.require: %w", pipeline, err)
		}
	}
	for _, raw := range g.Reject {
		if _, err := ParseGuardToken(raw); err != nil {
			return fmt.Errorf("pipeline %q: guards.reject: %w", pipeline, err)
		}
	}
	return nil
}

// Evaluate runs the guards against ctx. Reject fires (returns an
// error) when any reject token matches; Require fires when any
// require token does NOT match. Reject is evaluated first so a
// clear "you can't dispatch this" beats a vaguer "missing prereq"
// when both happen to apply.
//
// The returned error is suitable for direct surface to the
// operator -- it names the pipeline, the violated token, and
// (for arg: tokens) the actual resolved value.
func (g Guards) Evaluate(pipeline string, ctx GuardContext) error {
	for _, raw := range g.Reject {
		tok, _ := ParseGuardToken(raw) // pre-validated at load time
		if tok.Matches(ctx) {
			return fmt.Errorf("pipeline %q: dispatch rejected by guard %q", pipeline, raw)
		}
	}
	for _, raw := range g.Require {
		tok, _ := ParseGuardToken(raw)
		if !tok.Matches(ctx) {
			return fmt.Errorf("pipeline %q: dispatch requires %q (not satisfied by current profile/args)", pipeline, raw)
		}
	}
	return nil
}
