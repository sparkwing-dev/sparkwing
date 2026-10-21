package runspec

import (
	"fmt"
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
	add := func(raw string, list bool) error {
		if list {
			for _, item := range argvalue.List(raw) {
				if err := secrets.Validate(item); err != nil {
					return err
				}
				if !seen[item] {
					values = append(values, item)
					seen[item] = true
				}
			}
		}
		if err := secrets.Validate(raw); err != nil {
			return err
		}
		if !seen[raw] {
			values = append(values, raw)
			seen[raw] = true
		}
		return nil
	}
	for _, value := range projection.Defaults {
		if value.Raw == "" {
			continue
		}
		if err := add(value.Raw, value.List); err != nil {
			return SecretRegistration{}, fmt.Errorf("secret argument default: %w", err)
		}
	}
	names := append([]string(nil), resolved.SecretArgs...)
	names = append(names, request.SecretArgs.Value...)
	slices.Sort(names)
	names = slices.Compact(names)
	for _, name := range names {
		list := slices.Contains(projection.ListArgs, name)
		if raw, present := request.RequestedArgs[name]; present {
			if err := add(raw, list); err != nil {
				return SecretRegistration{}, fmt.Errorf("secret argument %q: %w", name, err)
			}
		}
		raw, present := resolved.Args[name]
		if present {
			if err := add(raw, list); err != nil {
				return SecretRegistration{}, fmt.Errorf("secret argument %q: %w", name, err)
			}
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
