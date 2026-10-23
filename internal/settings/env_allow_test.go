package settings

import (
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestSubmitEnvironmentPatternsResolve(t *testing.T) {
	want := []string{"APP_*", "EXACT", "AB_*"}
	got, _, err := submitEnvAllow(Inputs{Context: Context{Submitter: true}, Machine: Values{Options: runspec.Options{SubmitEnvAllow: present(want)}}})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%v, %v", got, err)
	}
	for _, pattern := range []string{"*", "APP*", "APP_*_MORE", "APP\nOTHER", "APP?", "1APP", "_*", "A_*"} {
		if _, _, err := submitEnvAllow(Inputs{Context: Context{Submitter: true}, Machine: Values{Options: runspec.Options{SubmitEnvAllow: present([]string{pattern})}}}); err == nil {
			t.Errorf("accepted %q", pattern)
		}
	}
}
