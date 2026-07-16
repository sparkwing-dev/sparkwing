package orchestrator

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }

func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func splitEnvList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseK8sNodeSelector(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(values))
	for _, raw := range values {
		key, val, ok := strings.Cut(raw, "=")
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if !ok || key == "" || val == "" {
			return nil, fmt.Errorf("node selector %q must be key=value", raw)
		}
		out[key] = val
	}
	return out, nil
}

func parseK8sTolerations(values []string) ([]corev1.Toleration, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]corev1.Toleration, 0, len(values))
	for _, raw := range values {
		body, effect, ok := strings.Cut(strings.TrimSpace(raw), ":")
		if !ok || strings.TrimSpace(effect) == "" {
			return nil, fmt.Errorf("toleration %q must be key[=value]:Effect", raw)
		}
		key, val, hasValue := strings.Cut(body, "=")
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		effect = strings.TrimSpace(effect)
		if key == "" {
			return nil, fmt.Errorf("toleration %q has empty key", raw)
		}
		tol := corev1.Toleration{Key: key, Effect: corev1.TaintEffect(effect)}
		if hasValue {
			if val == "" {
				return nil, fmt.Errorf("toleration %q has empty value", raw)
			}
			tol.Operator = corev1.TolerationOpEqual
			tol.Value = val
		} else {
			tol.Operator = corev1.TolerationOpExists
		}
		out = append(out, tol)
	}
	return out, nil
}
