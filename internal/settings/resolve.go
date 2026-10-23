package settings

import (
	"fmt"
	"maps"
	"strings"
)

// Error supplies a stable message for the shared apierr translation boundary.
func (e *Error) Error() string {
	location := string(e.From.Source)
	if e.From.File != "" {
		location += " " + e.From.File
	}
	if e.From.Line > 0 {
		location += fmt.Sprintf(":%d", e.From.Line)
	}
	message := fmt.Sprintf("%s (%s): %s", e.Key, location, e.Reason)
	if e.CeilingFrom != nil {
		message += fmt.Sprintf("; requested %s exceeds %s from %s %s", e.Value, e.Ceiling, e.CeilingFrom.Source, e.CeilingFrom.File)
	}
	return message
}

func ruleFor(key string) rule {
	for _, rule := range table() {
		if rule.Key == key {
			return rule
		}
	}
	return rule{}
}

func sourceValues(in Inputs, source Source) Values {
	switch source {
	case Flag:
		return in.Flags
	case Env:
		return in.Environment
	case sourceRun:
		return in.Sealed
	case projectPipeline:
		return in.Pipeline
	case projectDefaults:
		return in.Defaults
	case code:
		return in.Code
	case machine:
		return in.Machine
	default:
		return Values{}
	}
}

func originFor(v Values, source Source, key string) Origin {
	if origin, ok := v.Origins[key]; ok {
		if source != sourceRun {
			origin.Source = source
		}
		if origin.Key == "" {
			origin.Key = key
		}
		return origin
	}
	return Origin{Source: source, Key: key}
}

func sources(in Inputs, key string) []Source {
	rule := ruleFor(key)
	if !in.Context.Submitter && rule.Phase != startup {
		return []Source{sourceRun}
	}
	return rule.Sources
}

func pick[T any](in Inputs, key string, field func(Values) *T, fallback T) (T, Origin, error) {
	if ruleFor(key).Key == "" {
		var zero T
		return zero, Origin{}, fmt.Errorf("setting %s is absent from source table", key)
	}
	for _, source := range sources(in, key) {
		v := sourceValues(in, source)
		if value := field(v); value != nil {
			return *value, originFor(v, source, key), nil
		}
	}
	if !in.Context.Submitter && ruleFor(key).Phase != startup {
		var zero T
		return zero, Origin{Source: sourceRun, Key: key}, invalid(key, Origin{Source: sourceRun, Key: key}, "sealed setting is absent")
	}
	return fallback, Origin{Source: Default, Key: key}, nil
}

func invalid(key string, origin Origin, reason string) error {
	return &Error{Key: key, From: origin, Reason: reason}
}

func above(key string, origin Origin, value, ceiling any, from Origin) error {
	return &Error{Key: key, From: origin, CeilingFrom: &from, Value: fmt.Sprint(value), Ceiling: fmt.Sprint(ceiling), Reason: "above operator ceiling"}
}

func mergeArgs(in Inputs) (map[string]string, Origin, error) {
	out := map[string]string{}
	origin := Origin{Source: Default, Key: "args"}
	order := sources(in, "args")
	found := false
	for i := len(order) - 1; i >= 0; i-- {
		source := order[i]
		if source == Flag {
			maps.Copy(out, in.Context.ScheduleArgs)
			maps.Copy(out, in.Context.ScheduleHostArgs)
		}
		v := sourceValues(in, source)
		if v.Args != nil {
			maps.Copy(out, v.Args)
			origin = originFor(v, source, "args")
			found = true
		}
	}
	if !in.Context.Submitter && !found {
		return nil, origin, invalid("args", Origin{Source: sourceRun, Key: "args"}, "sealed setting is absent")
	}
	return out, origin, nil
}

func validNames(key string, values []string, origin Origin) error {
	for _, name := range values {
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\x00\r\n") {
			return invalid(key, origin, "names must be nonempty and contain no control characters")
		}
	}
	return nil
}
