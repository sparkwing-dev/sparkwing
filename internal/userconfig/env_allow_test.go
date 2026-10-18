package userconfig

import (
	"reflect"
	"strings"
	"testing"
)

func TestSubmitEnvironmentPatternsDecode(t *testing.T) {
	cfg, err := Decode("machine.yaml", []byte("run:\n  submit_env_allow: [APP_*, EXACT, AB_*]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Run.SubmitEnvAllow, []string{"APP_*", "EXACT", "AB_*"}) {
		t.Fatal(cfg.Run.SubmitEnvAllow)
	}
	for _, pattern := range []string{"*", "APP*", "APP_*_OTHER", "APP_**", "1APP", "APP=VALUE"} {
		_, err := Decode("machine.yaml", []byte("run:\n  submit_env_allow: ['"+pattern+"']\n"))
		if err == nil {
			t.Errorf("accepted %q", pattern)
			continue
		}
		if !strings.Contains(err.Error(), "machine.yaml") || !strings.Contains(err.Error(), "submit_env_allow") || !strings.Contains(err.Error(), "line 2") {
			t.Fatalf("missing error location: %v", err)
		}
	}
}

func TestSubmitEnvironmentRefusesTooBroadPrefixes(t *testing.T) {
	for _, pattern := range []string{"_*", "__*", "A_*", "_A_*"} {
		_, err := Decode("machine.yaml", []byte("run:\n  submit_env_allow: ['"+pattern+"']\n"))
		if err == nil || !strings.Contains(err.Error(), "too broad") {
			t.Errorf("%q: err = %v, want a too-broad refusal", pattern, err)
		}
	}
}
