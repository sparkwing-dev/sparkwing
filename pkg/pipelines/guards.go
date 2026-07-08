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
	Arg  string // for KindProfileName / KindArg / KindGitBranch: the LHS / branch name
	Val  string // for KindArg: the RHS literal
}

// GuardKind is the parsed predicate variant.
type GuardKind int

const (
	KindUnknown GuardKind = iota
	KindProfileLocal
	KindProfileController
	KindProfileName    // profile:name=NAME
	KindGitBranch      // git:branch=NAME
	KindGitBranchOnDef // git:branch=default
	KindArg            // arg:FLAG=VALUE
)

// GuardContext is everything a guard token needs to evaluate: the
// active profile shape + the resolved args by CLI flag name.
//
// The evaluator never reaches back into orchestrator or profile
// packages -- callers pack what they have into this context and
// hand it off. Keeps guards/predicates testable in isolation.
type GuardContext struct {
	// ProfileName is the active profile's name. Empty when no
	// profile is in scope (no --profile, no pipeline.profile, no
	// defaults.profile).
	ProfileName string

	// ProfileIsLocal is true when the active profile routes
	// in-process (no controller URL).
	ProfileIsLocal bool

	// Args is the resolved args map keyed by CLI flag name.
	// Values are stringified for comparison. Nil-safe.
	Args map[string]string

	// GitBranch is the branch the dispatch originated on. Empty
	// when no git context is in scope (cluster-side replays, etc.);
	// git:branch tokens never match an empty branch so guards stay
	// safe by default.
	GitBranch string

	// GitDefaultBranch is the repo's default branch (typically main
	// or master). Used by git:branch=default. Empty when no git
	// context is in scope.
	GitDefaultBranch string
}

// ParseGuardToken parses one flat token string into a GuardToken
// ready for evaluation. Returns an error naming the unknown form
// when the token doesn't match a known shape.
//
// Vocabulary (case-sensitive):
//
//	profile:local            -- active profile has no controller
//	profile:controller       -- active profile has a controller URL
//	profile:name=NAME        -- active profile's name equals NAME
//	git:branch=default       -- dispatch on the repo's default branch
//	git:branch=NAME          -- dispatch on the named branch
//	arg:FLAG=VALUE           -- resolved arg equals the value
func ParseGuardToken(raw string) (GuardToken, error) {
	tok := GuardToken{Raw: raw}
	namespace, rest, hasColon := strings.Cut(raw, ":")
	if !hasColon {
		return tok, fmt.Errorf("guard %q: expected namespace:rest (e.g. profile:controller, git:branch=main, arg:flag=value)", raw)
	}
	switch namespace {
	case "profile":
		return parseProfileGuard(raw, rest)
	case "git":
		return parseGitGuard(raw, rest)
	case "arg":
		return parseArgGuard(raw, rest)
	default:
		return tok, fmt.Errorf("guard %q: unknown namespace %q (valid: profile, git, arg)", raw, namespace)
	}
}

func parseProfileGuard(raw, rest string) (GuardToken, error) {
	tok := GuardToken{Raw: raw}
	switch rest {
	case "local":
		tok.Kind = KindProfileLocal
		return tok, nil
	case "controller":
		tok.Kind = KindProfileController
		return tok, nil
	}
	key, val, ok := strings.Cut(rest, "=")
	if !ok || key != "name" || val == "" {
		return tok, fmt.Errorf("guard %q: profile: expects 'local', 'controller', or 'name=NAME'", raw)
	}
	tok.Kind = KindProfileName
	tok.Arg = val
	return tok, nil
}

func parseGitGuard(raw, rest string) (GuardToken, error) {
	tok := GuardToken{Raw: raw}
	key, val, ok := strings.Cut(rest, "=")
	if !ok || key != "branch" || val == "" {
		return tok, fmt.Errorf("guard %q: git: expects 'branch=NAME' or 'branch=default'", raw)
	}
	if val == "default" {
		tok.Kind = KindGitBranchOnDef
		return tok, nil
	}
	tok.Kind = KindGitBranch
	tok.Arg = val
	return tok, nil
}

func parseArgGuard(raw, rest string) (GuardToken, error) {
	tok := GuardToken{Raw: raw}
	flag, val, ok := strings.Cut(rest, "=")
	if !ok || flag == "" || val == "" {
		return tok, fmt.Errorf("guard %q: arg: expects FLAG=VALUE", raw)
	}
	tok.Kind = KindArg
	tok.Arg = flag
	tok.Val = val
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
func (g Guards) Evaluate(pipeline string, ctx GuardContext) error {
	for _, raw := range g.Reject {
		tok, _ := ParseGuardToken(raw)
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

// IsEmpty reports whether the guards block declares no tokens. Used
// at resolution time to fall through to defaults.guards when a
// pipeline declares its own (wholesale replace) only when non-empty.
func (g Guards) IsEmpty() bool {
	return len(g.Require) == 0 && len(g.Reject) == 0
}
