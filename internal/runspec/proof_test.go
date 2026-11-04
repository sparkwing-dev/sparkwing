package runspec

import (
	"reflect"
	"strings"
	"testing"
)

// safety: a new run option must be classified here, so it cannot reach a counted pass unnoticed.
func TestEveryRunOptionIsClassifiedForProof(t *testing.T) {
	disqualifying := map[string]bool{"checkout": true, "start_at": true, "stop_at": true, "only": true, "dry_run": true, "allow_risk": true, "pause": true, "debug": true, "publish": true, "cache_from": true, "job_defaults": true}
	fields := reflect.TypeFor[Options]()
	for i := range fields.NumField() {
		name, _, _ := strings.Cut(fields.Field(i).Tag.Get("json"), ",")
		if proofNeutralOptions[name] == disqualifying[name] {
			t.Errorf("option %q must be either proof neutral or disqualifying", name)
		}
	}
}

func TestProofEligible(t *testing.T) {
	for name, tc := range map[string]struct {
		set      func(*Request)
		eligible bool
	}{
		"no options":      {func(*Request) {}, true},
		"neutral workers": {func(r *Request) { r.Options.Workers = Setting[int]{Optional: Optional[int]{Set: true, Value: 4}} }, true},
		"dry run present but false": {func(r *Request) {
			r.Options.DryRun = Setting[bool]{Optional: Optional[bool]{Set: true}, Origin: SettingOrigin{Layer: LayerDefault}}
		}, true},
		"only":               {func(r *Request) { r.Options.Only.Value = "lint" }, false},
		"pause before a job": {func(r *Request) { r.Options.Pause.Value.Before.Value = []string{"test"} }, false},
		"job retries":        {func(r *Request) { r.Options.JobDefaults.Retry.Value = 3 }, false},
		"sealed environment": {func(r *Request) { r.Env.Names = []string{"GOFLAGS"} }, false},
	} {
		var r Request
		tc.set(&r)
		if got := r.ProofEligible(); got != tc.eligible {
			t.Errorf("%s: ProofEligible = %v, want %v", name, got, tc.eligible)
		}
	}
}
