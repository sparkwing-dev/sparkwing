package nodeproto

import (
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/argvalue"
)

// ValidateArgument checks a supplied value and returns its canonical representation.
func ValidateArgument(arg ArgumentDescription, value string) (string, error) {
	if arg.Type == "[]string" {
		value = strings.Join(argvalue.List(value), ",")
	}
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
		values = argvalue.List(value)
	}
	for _, item := range values {
		if len(arg.Enum) > 0 && !slices.ContainsFunc(arg.Enum, func(choice string) bool { return strings.TrimSpace(choice) == item }) {
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
	out := ApplyArgumentDefaults(description.Args, raw)
	names := map[string]bool{}
	for _, arg := range description.Args {
		names[arg.Name] = true
		value, present := out[arg.Name]
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

// ApplyArgumentDefaults copies supplied values and fills only nonempty defaults.
func ApplyArgumentDefaults(arguments []ArgumentDescription, raw map[string]string) map[string]string {
	out := maps.Clone(raw)
	if out == nil {
		out = map[string]string{}
	}
	for _, argument := range arguments {
		if _, present := out[argument.Name]; !present && argument.Default != nil && *argument.Default != "" {
			out[argument.Name] = *argument.Default
		}
	}
	return out
}

// PrepareJobArguments prepares late declarations without rejecting other jobs' inputs.
func PrepareJobArguments(arguments []ArgumentDescription, raw map[string]string) (map[string]string, error) {
	if err := ValidateArgumentDeclarations(arguments); err != nil {
		return nil, &apierr.Error{Code: apierr.InvalidArgs, Message: err.Error()}
	}
	args, err := ResolveArguments(PipelineDescription{Args: arguments, Extra: true}, raw)
	if err != nil {
		return nil, &apierr.Error{Code: apierr.InvalidArgs, Message: err.Error()}
	}
	return args, nil
}

// ValidateJobArgumentNamespace keeps late declarations in the discovered flag namespace.
func ValidateJobArgumentNamespace(static []ArgumentDescription, jobs []JobSpec) error {
	arguments := slices.Clone(static)
	var jobArguments []ArgumentDescription
	for _, job := range jobs {
		jobArguments = append(jobArguments, job.Arguments...)
		for _, argument := range job.Arguments {
			if !slices.ContainsFunc(static, func(known ArgumentDescription) bool {
				if known.JobID == "" {
					return false
				}
				// Discovery uses zero inputs, so invocation inputs can change job IDs.
				known.JobID = argument.JobID
				return reflect.DeepEqual(known, argument)
			}) {
				arguments = append(arguments, argument)
			}
		}
	}
	if err := ValidateArgumentDeclarations(jobArguments); err != nil {
		return &apierr.Error{Code: apierr.InvalidArgs, Message: err.Error()}
	}
	if err := ValidateArgumentDeclarations(arguments); err != nil {
		return &apierr.Error{Code: apierr.InvalidArgs, Message: err.Error()}
	}
	return nil
}
