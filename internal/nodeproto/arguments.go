package nodeproto

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

func normalizeArgument(kind, value string) string {
	if kind != "[]string" {
		return value
	}
	items := []string{}
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return strings.Join(items, ",")
}

// ValidateArgument checks a supplied value and returns its canonical representation.
func ValidateArgument(arg ArgumentDescription, value string) (string, error) {
	value = normalizeArgument(arg.Type, value)
	var err error
	switch arg.Type {
	case "string", "[]string":
	case "bool":
		_, err = strconv.ParseBool(value)
	case "int", "int64":
		_, err = strconv.ParseInt(value, 10, 64)
	case "float64":
		var number float64
		number, err = strconv.ParseFloat(value, 64)
		if math.IsNaN(number) || math.IsInf(number, 0) {
			err = fmt.Errorf("non-finite number")
		}
	case "duration":
		_, err = time.ParseDuration(value)
	default:
		err = fmt.Errorf("unknown type")
	}
	if err != nil {
		return "", fmt.Errorf("argument --%s must be %s", arg.Name, arg.Type)
	}
	values := []string{value}
	if arg.Type == "[]string" {
		values = nil
		if value != "" {
			values = strings.Split(value, ",")
		}
	}
	for _, item := range values {
		if len(arg.Enum) > 0 && !slices.Contains(arg.Enum, item) {
			return "", fmt.Errorf("argument --%s is not an allowed value", arg.Name)
		}
	}
	return value, nil
}

// ValidateArgumentDeclarations checks the SDK's language-neutral declarations.
func ValidateArgumentDeclarations(args []ArgumentDescription) error {
	names, shorts := map[string]bool{}, map[string]bool{}
	for _, arg := range args {
		if arg.Name == "" || names[arg.Name] {
			return fmt.Errorf("SDK describe has ambiguous arguments")
		}
		names[arg.Name] = true
		if arg.Short != "" {
			if len(arg.Short) != 1 || arg.Short[0] > 127 || shorts[arg.Short] {
				return fmt.Errorf("argument --%s has an invalid or duplicate short name", arg.Name)
			}
			shorts[arg.Short] = true
		}
		switch arg.Type {
		case "string", "bool", "int", "int64", "float64", "duration", "[]string":
		default:
			return fmt.Errorf("argument --%s has an unknown type", arg.Name)
		}
		hasDefault := arg.Default != nil && *arg.Default != ""
		if arg.Required && hasDefault {
			return fmt.Errorf("argument --%s cannot be required and have a default", arg.Name)
		}
		if len(arg.Enum) > 0 {
			if arg.Type != "string" && arg.Type != "[]string" {
				return fmt.Errorf("argument --%s: enum requires string or []string", arg.Name)
			}
			if !arg.Required && !hasDefault {
				return fmt.Errorf("argument --%s: enum requires a default or required argument", arg.Name)
			}
		}
		if hasDefault {
			if _, err := ValidateArgument(arg, *arg.Default); err != nil {
				return err
			}
		}
	}
	return nil
}

// ResolveArguments applies declared defaults and validates host preparation inputs.
func ResolveArguments(description PipelineDescription, raw map[string]string) (map[string]string, error) {
	return resolveArguments(description, raw, true)
}

// ArgumentDefaults prepares declared defaults for inspection before invocation values exist.
func ArgumentDefaults(description PipelineDescription) (map[string]string, error) {
	return resolveArguments(description, nil, false)
}

func resolveArguments(description PipelineDescription, raw map[string]string, requireInputs bool) (map[string]string, error) {
	out := maps.Clone(raw)
	if out == nil {
		out = map[string]string{}
	}
	names := map[string]bool{}
	for _, arg := range description.Args {
		names[arg.Name] = true
		value, present := out[arg.Name]
		if !present && arg.Default != nil && *arg.Default != "" {
			value, present = *arg.Default, true
		}
		if !present {
			if arg.Required && requireInputs {
				return nil, fmt.Errorf("argument --%s is required", arg.Name)
			}
			continue
		}
		normalized, err := ValidateArgument(arg, value)
		if err != nil {
			return nil, err
		}
		out[arg.Name] = normalized
	}
	if !description.Extra {
		for _, name := range slices.Sorted(maps.Keys(raw)) {
			if !names[name] {
				return nil, fmt.Errorf("no flag --%s is declared", name)
			}
		}
	}
	return out, nil
}
