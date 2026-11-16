package procenv

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestProfileSelectorsCannotBeSubmitted(t *testing.T) {
	for _, name := range []string{"APPDATA", "HOMEDRIVE", "HOMEPATH", "XDG_DATA_HOME", "PSModulePath"} {
		names := []string{name}
		if runtime.GOOS == "windows" {
			names = append(names, strings.ToLower(name), strings.ToUpper(name))
		}
		for _, name := range names {
			t.Run(name, func(t *testing.T) {
				values := map[string][]byte{name: []byte("requester-selector")}
				if err := CheckSubmitted(values, secrets.NewMasker()); err == nil {
					t.Error("profile selector submission was accepted")
				}
				captured, err := Submit([]string{name + "=requester-selector"}, []string{name}, secrets.NewMasker())
				if err != nil || len(captured) != 0 {
					t.Errorf("client captured a reserved selector: %v %v", captured, err)
				}
				for _, present := range []bool{false, true} {
					base := Base{Masker: secrets.NewMasker(), SubmitEnvAllow: []string{name}}
					if present {
						base.Ambient = []string{name + "=host-selector"}
					}
					if _, err := WithSubmitted(base, values, SubmitAcceptAll()); err == nil {
						t.Errorf("host accepted selector with host-set=%t", present)
					}
				}
			})
		}
	}
}

func TestProfileSelectorsFollowHostInheritancePolicy(t *testing.T) {
	for _, name := range []string{"APPDATA", "HOMEDRIVE", "HOMEPATH", "XDG_DATA_HOME", "PSModulePath"} {
		for _, tc := range []struct {
			kind          Kind
			allowed, want bool
		}{
			{CLI, false, true},
			{Plan, false, false},
			{Plan, true, true},
			{Attempt, false, false},
			{Attempt, true, true},
			{Eval, false, false},
			{Eval, true, true},
			{Install, false, false},
			{Install, true, true},
			{Git, false, false},
			{Git, true, false},
		} {
			t.Run(fmt.Sprintf("%s/%d/%t", name, tc.kind, tc.allowed), func(t *testing.T) {
				base := Base{Ambient: []string{name + "=host-selector"}, Captured: []string{name + "=requester-selector"}, Masker: secrets.NewMasker()}
				if tc.allowed {
					base.SubmitEnvAllow = []string{name}
				}
				env, err := Build(tc.kind, base, Runtime{})
				if err != nil {
					t.Fatal(err)
				}
				value, present := env.LookupEnv(name)
				if present != tc.want || present && value != "host-selector" {
					t.Fatalf("selector value=%q present=%t, want host-owned present=%t", value, present, tc.want)
				}
			})
		}
		env, err := Build(Plan, Base{Local: true, Captured: []string{name + "=local-selector"}, Masker: secrets.NewMasker()}, Runtime{})
		if err != nil || env.Lookup(name) != "local-selector" {
			t.Fatalf("local capture changed for %s: %v", name, err)
		}
	}
}

func TestPowerShellModulePathSubmissionIsCaseInsensitive(t *testing.T) {
	for _, name := range []string{"PSModulePath", "PSMODULEPATH", "psmodulepath", "PsMoDuLePaTh"} {
		if err := CheckSubmitted(map[string][]byte{name: []byte("requester-modules")}, secrets.NewMasker()); err == nil {
			t.Errorf("code-loading selector %s was accepted", name)
		}
	}
}
