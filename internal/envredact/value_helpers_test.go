package envredact

import (
	"encoding/json"
	"strings"
)

// credentialValue reports whether a value carries a credential that
// cannot be rewritten in place: a bearer header, a PEM block, a JSON
// document or array with a credential-shaped field, a KEY=VALUE
// assignment named like one, or a URL whose query or path names one.
// Every line of a multi-line value is examined. Such values must be
// dropped.
func credentialValue(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return false
	}
	if strings.Contains(v, "-----BEGIN ") {
		return true
	}
	if credentialJSON(v) {
		return true
	}
	for _, line := range strings.Split(v, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if bearerIn(line) || credentialJSON(line) || credentialURL(line) || credentialAssignmentIn(line) {
			return true
		}
	}
	return false
}

func credentialJSON(value string) bool {
	if !strings.HasPrefix(value, "{") && !strings.HasPrefix(value, "[") {
		return false
	}
	var doc any
	if json.Unmarshal([]byte(value), &doc) != nil {
		return false
	}
	return credentialFieldIn(doc, jsonScanDepth)
}

func credentialFieldIn(doc any, depth int) bool {
	if depth <= 0 {
		return false
	}
	switch node := doc.(type) {
	case map[string]any:
		for k, v := range node {
			if credentialShaped(k) || credentialFieldIn(v, depth-1) {
				return true
			}
		}
	case []any:
		for _, v := range node {
			if credentialFieldIn(v, depth-1) {
				return true
			}
		}
	}
	return false
}
