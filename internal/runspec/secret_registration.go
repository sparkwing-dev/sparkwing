package runspec

import (
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/argvalue"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// BuildSecretRegistration derives the accepted run's complete argument mask set.
// The caller supplies discovered literal inputs and selected list argument
// names. The projection is checked again on seal read.
func BuildSecretRegistration(request Request, resolved Resolved) (SecretRegistration, error) {
	projection := resolved.SecretRegistration
	projection.Version = 1
	var values []string
	seen := make(map[string]bool)
	add := func(raw string, list bool) {
		for _, value := range secretCandidates(raw, list) {
			if !seen[value] {
				values = append(values, value)
				seen[value] = true
			}
		}
	}
	for _, value := range projection.Defaults {
		if value.Raw == "" {
			continue
		}
		add(value.Raw, value.List)
	}
	names := append([]string(nil), resolved.SecretArgs...)
	names = append(names, request.SecretArgs.Value...)
	slices.Sort(names)
	names = slices.Compact(names)
	for _, name := range names {
		list := slices.Contains(projection.ListArgs, name)
		if raw, present := request.RequestedArgs[name]; present {
			add(raw, list)
		}
		raw, present := resolved.Args[name]
		if present {
			add(raw, list)
		}
	}
	projection.Values = values
	return projection, nil
}

// GeneratedRegistration seals literal inputs so expansion replay needs no argument reconstruction.
func GeneratedRegistration(request Request, resolved Resolved, arguments []nodeproto.ArgumentDescription) (SecretRegistration, error) {
	projection := SecretRegistration{}
	for _, argument := range arguments {
		if !argument.Secret && !slices.Contains(resolved.SecretArgs, argument.Name) && !slices.Contains(request.SecretArgs.Value, argument.Name) {
			continue
		}
		list := argument.Type == "[]string"
		if argument.Default != nil && *argument.Default != "" {
			projection.Defaults = append(projection.Defaults, SecretValue{Raw: *argument.Default, List: list})
		}
		for _, args := range []map[string]string{request.RequestedArgs, resolved.Args} {
			if raw, ok := args[argument.Name]; ok {
				projection.Defaults = append(projection.Defaults, SecretValue{Raw: raw, List: list})
			}
		}
	}
	return BuildSecretRegistration(Request{}, Resolved{SecretRegistration: projection})
}

func secretCandidates(raw string, list bool) []string {
	if raw == "" {
		return nil
	}
	var values []string
	if list {
		values = argvalue.List(raw)
	}
	return append(values, raw)
}

// Warning reports accepted argument values that cannot be masked in logs.
func (r SecretRegistration) Warning() string {
	for _, value := range r.Values {
		if !secrets.Maskable(value) {
			return secrets.ShortValueWarning
		}
	}
	return ""
}
