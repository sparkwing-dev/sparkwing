package procenv

import (
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// Submit captures only present, permitted user variables and registers their masks.
func Submit(environment, allowed []string, masker *secrets.Masker) (map[string][]byte, error) {
	if err := ValidateSubmitEnvAllow(allowed); err != nil {
		return nil, err
	}
	values := map[string][]byte{}
	seen := map[string]bool{}
	for _, entry := range environment {
		name, value, err := parseEntry(entry)
		if err != nil {
			return nil, err
		}
		if !submittable(name) || !submitAllowed(name, allowed) {
			continue
		}
		if seen[envKey(name)] {
			return nil, errors.New("submitted environment contains duplicate names")
		}
		seen[envKey(name)] = true
		if err := registerMasks(masker, name, value, true); err != nil {
			return nil, err
		}
		values[name] = []byte(value)
	}
	return values, nil
}

// safety: a submitted value comes from the requester, so it may not reconfigure the executing
// host, nor load code into a job or tool. Names the registry owns are refused unless listed in
// safeToolNames, and unregistered names in a code-loading family are refused in any case.
func submittable(name string) bool {
	if !submitEnvName.MatchString(name) {
		return false
	}
	row, known := Lookup(name)
	if row.Owner == "engine" || credentialName(row, name) {
		return false
	}
	upper := strings.ToUpper(name)
	if known && row.Class != User {
		return slices.Contains(safeToolNames, upper)
	}
	if strings.HasPrefix(upper, "SPARKWING_") || goEnvName.MatchString(upper) || slices.Contains(loaderNames, upper) {
		return false
	}
	return !slices.ContainsFunc(loaderPrefixes, func(prefix string) bool { return strings.HasPrefix(upper, prefix) })
}

var (
	safeToolNames  = []string{"AWS_REGION", "AWS_DEFAULT_REGION", "NO_COLOR", "CLICOLOR_FORCE", "TERM"}
	goEnvName      = regexp.MustCompile(`^GO[A-Z0-9]*$`)
	loaderNames    = []string{"ENV", "BASH_ENV", "CDPATH", "IFS", "PROMPT_COMMAND", "JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS", "JDK_JAVA_OPTIONS", "RUBYOPT", "PERL5OPT", "PERL5LIB", "PYTHONPATH", "PYTHONSTARTUP", "PYTHONHOME"}
	loaderPrefixes = []string{"LD_", "DYLD_", "GIT_", "SSH_", "CGO_", "NODE_", "NPM_", "YARN_", "PIP_", "PNPM_", "BUN_", "CARGO_", "RUSTC", "BUNDLE_", "GEM_", "MAVEN_", "GRADLE_"}
)

func submitAllowed(name string, allowed []string) bool {
	for _, pattern := range allowed {
		if envKey(pattern) == envKey(name) || strings.HasSuffix(pattern, "_*") && strings.HasPrefix(envKey(name), envKey(strings.TrimSuffix(pattern, "*"))) {
			return true
		}
	}
	return false
}

// SubmitAccept is the executing host operator's policy for requester-submitted names.
type SubmitAccept struct {
	patterns []string
	every    bool
}

// OperatorSubmitAccept accepts the submitted names the host operator lists in its own
// run.submit_env_allow. An empty list accepts none.
func OperatorSubmitAccept(patterns []string) (SubmitAccept, error) {
	if err := ValidateSubmitEnvAllow(patterns); err != nil {
		return SubmitAccept{}, err
	}
	return SubmitAccept{patterns: slices.Clone(patterns)}, nil
}

// SubmitAcceptAll accepts every submittable name: for the submitting machine itself, or
// for a sandbox that serves one claim and is then discarded, so no value outlives the claim.
func SubmitAcceptAll() SubmitAccept { return SubmitAccept{every: true} }

func (a SubmitAccept) accepts(name string) bool { return a.every || submitAllowed(name, a.patterns) }

// CheckSubmitted refuses a reserved, refused or duplicate submitted name and registers value masks.
func CheckSubmitted(values map[string][]byte, masker *secrets.Masker) error {
	seen := map[string]bool{}
	for name, value := range values {
		if !submittable(name) || strings.ContainsRune(string(value), 0) || seen[envKey(name)] {
			return errors.New("submitted environment contains a reserved or invalid variable")
		}
		seen[envKey(name)] = true
		if err := registerMasks(masker, name, string(value), true); err != nil {
			return err
		}
	}
	return nil
}

// WithSubmitted binds private submitted values to a remote base before any process starts.
// Only names the host operator accepts reach the base; the rest are dropped. A name the
// host already sets keeps the host's value, and installs never receive submitted values.
func WithSubmitted(base Base, values map[string][]byte, accept SubmitAccept) (Base, error) {
	if base.Local {
		return Base{}, errors.New("submitted values require a remote environment")
	}
	if err := CheckSubmitted(values, base.Masker); err != nil {
		return Base{}, err
	}
	hostSet := map[string]bool{}
	for _, entry := range base.Ambient {
		name, _, _ := strings.Cut(entry, "=")
		hostSet[envKey(name)] = true
	}
	base.Ambient, base.Submitted = slices.Clone(base.Ambient), slices.Clone(base.Submitted)
	for name, value := range values {
		if !accept.accepts(name) || hostSet[envKey(name)] {
			continue
		}
		base.Ambient = append(base.Ambient, name+"="+string(value))
		base.Submitted = append(base.Submitted, name)
	}
	return base, nil
}
