package procenv

import (
	"errors"
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
		row, _ := Lookup(name)
		if row.Owner == "engine" || credentialName(row, name) || !submitAllowed(name, allowed) {
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

func submitAllowed(name string, allowed []string) bool {
	for _, pattern := range allowed {
		if envKey(pattern) == envKey(name) || strings.HasSuffix(pattern, "_*") && strings.HasPrefix(envKey(name), envKey(strings.TrimSuffix(pattern, "*"))) {
			return true
		}
	}
	return false
}

// WithSubmitted binds private submitted values to a remote base before any process starts.
func WithSubmitted(base Base, values map[string][]byte) (Base, error) {
	if base.Local {
		return Base{}, errors.New("submitted values require a remote environment")
	}
	base.Ambient, base.SubmitEnvAllow = slices.Clone(base.Ambient), slices.Clone(base.SubmitEnvAllow)
	seen := map[string]bool{}
	for name, value := range values {
		row, _ := Lookup(name)
		if !submitEnvName.MatchString(name) || row.Owner == "engine" || credentialName(row, name) || strings.ContainsRune(string(value), 0) || seen[envKey(name)] {
			return Base{}, errors.New("submitted environment contains a reserved or invalid variable")
		}
		seen[envKey(name)] = true
		if err := registerMasks(base.Masker, name, string(value), true); err != nil {
			return Base{}, err
		}
		base.Ambient = append(base.Ambient, name+"="+string(value))
		base.SubmitEnvAllow = append(base.SubmitEnvAllow, name)
	}
	return base, nil
}
