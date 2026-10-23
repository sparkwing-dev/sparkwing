package procenv

import (
	"fmt"
	"iter"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// Env is an immutable process environment produced by Build. Its zero value is empty.
type Env struct {
	values []string
	masker *secrets.Masker
	remote bool
}

// Values iterates assignments without exposing the environment's backing storage.
func (e Env) Values() iter.Seq[string] { return slices.Values(e.values) }

// Lookup returns a variable's value using the platform's name comparison.
func (e Env) Lookup(name string) string {
	value, _ := e.LookupEnv(name)
	return value
}

// LookupEnv returns a value and its presence using the platform's name comparison.
func (e Env) LookupEnv(name string) (string, bool) {
	for i := len(e.values) - 1; i >= 0; i-- {
		key, value, _ := strings.Cut(e.values[i], "=")
		if envKey(key) == envKey(name) {
			return value, true
		}
	}
	return "", false
}

// GoTool applies Go-tool execution settings through the same credential and mask policy.
func (e Env) GoTool(settings ...string) (Env, error) {
	result := e
	result.values = slices.Clone(e.values)
	for _, setting := range settings {
		name, value, err := parseEntry(setting)
		if err != nil {
			return Env{}, err
		}
		row, _ := Lookup(name)
		if !(row.GoTool || slices.Contains([]string{"GOWORK", "GOOS", "GOARCH"}, name)) || credentialName(row, name) {
			return Env{}, fmt.Errorf("environment variable %q is not a public Go-tool setting", name)
		}
		if err := registerMasks(e.masker, name, value, e.remote); err != nil {
			return Env{}, err
		}
		result.values = slices.DeleteFunc(result.values, func(entry string) bool {
			key, _, _ := strings.Cut(entry, "=")
			return envKey(key) == envKey(name)
		})
		result.values = append(result.values, setting)
	}
	return result, nil
}
