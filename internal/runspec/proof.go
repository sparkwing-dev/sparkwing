package runspec

import (
	"reflect"
	"strings"
)

// safety: proofNeutralOptions only place, schedule or present a run. A run that sets any other option,
// including one added later, may have judged something other than the committed code, so it never counts.
var proofNeutralOptions = map[string]bool{
	"runner": true, "priority": true, "admission_class": true, "claim_wait": true,
	"owner_execution_timeout": true, "requires": true, "prefers": true, "no_memo": true,
	"workers": true, "output": true, "pause_timeout": true, "submit_env_allow": true,
	"repo_location": true, "profile": true,
}

// ProofEligible reports whether a run of r may count toward a pass that
// sw.FromProvenRunAtCommit reads: it seals no environment variables and sets no
// option outside proofNeutralOptions.
func (r Request) ProofEligible() bool {
	if len(r.Env.Names) > 0 {
		return false
	}
	options := reflect.ValueOf(r.Options)
	for i := range options.NumField() {
		name, _, _ := strings.Cut(options.Type().Field(i).Tag.Get("json"), ",")
		if !proofNeutralOptions[name] && !unsetValue(options.Field(i)) {
			return false
		}
	}
	return true
}

// safety: presence and origin are ignored because a defaulted option still records both; only a value counts.
func unsetValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			if name := v.Type().Field(i).Name; name != "Set" && name != "Origin" && name != "Origins" && !unsetValue(v.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Slice, reflect.Map:
		return v.Len() == 0
	default:
		return v.IsZero()
	}
}
