package settings

import (
	"reflect"
	"testing"
)

func TestSubmitEnvironmentPatternsResolve(t *testing.T) {
	want := []string{"APP_*", "EXACT", "_*"}
	got, _, err := SubmitEnvAllow(Inputs{Context: Context{Submitter: true}, Machine: Values{SubmitEnvAllow: want}})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%v, %v", got, err)
	}
	for _, pattern := range []string{"*", "APP*", "APP_*_MORE", "APP\nOTHER", "APP?", "1APP"} {
		if _, _, err := SubmitEnvAllow(Inputs{Context: Context{Submitter: true}, Machine: Values{SubmitEnvAllow: []string{pattern}}}); err == nil {
			t.Errorf("accepted %q", pattern)
		}
	}
}
