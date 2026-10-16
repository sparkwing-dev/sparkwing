package store

import (
	"errors"
)

// InvocationSecretArgsKey labels argument classification in the reporting view.
const InvocationSecretArgsKey = "secret_args"

// ErrSecretInputHash refuses a legacy input hash over supplied secret arguments.
var ErrSecretInputHash = errors.New("inputs_hash is forbidden when a secret argument is supplied")

// ValidateRunInvocation refuses secret-bearing input hashes before raw run insertion.
func ValidateRunInvocation(r Run) error {
	if _, ok := r.Invocation["inputs_hash"]; !ok {
		return nil
	}
	raw, classified := r.Invocation[InvocationSecretArgsKey]
	if !classified {
		return nil
	}
	names, valid := decodeStringSliceChecked(raw)
	if !valid {
		return ErrSecretInputHash
	}
	invArgs := invocationArgs(r.Invocation)
	for _, name := range names {
		if _, ok := r.Args[name]; ok {
			return ErrSecretInputHash
		}
		if _, ok := invArgs[name]; ok {
			return ErrSecretInputHash
		}
	}
	return nil
}

func decodeStringSliceChecked(v any) ([]string, bool) {
	switch typed := v.(type) {
	case []string:
		if len(typed) == 0 {
			return nil, true
		}
		out := make([]string, 0, len(typed))
		for _, s := range typed {
			if s == "" {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			s, ok := item.(string)
			if !ok || s == "" {
				return nil, false
			}
			out = append(out, s)
		}
		if len(out) == 0 {
			return nil, true
		}
		return out, true
	default:
		return nil, false
	}
}

func invocationArgs(inv map[string]any) map[string]string {
	switch typed := inv["args"].(type) {
	case map[string]string:
		return typed
	case map[string]any:
		out := make(map[string]string, len(typed))
		for k, v := range typed {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
		return out
	default:
		return nil
	}
}
