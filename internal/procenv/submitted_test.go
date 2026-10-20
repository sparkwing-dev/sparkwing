package procenv

import (
	"encoding/hex"
	"fmt"
	"maps"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func FuzzSubmittedEnvironmentFiltersAndMasks(f *testing.F) {
	f.Add([]byte("private input"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 256 {
			return
		}
		value := "submitted-value-" + hex.EncodeToString(raw)
		masker := secrets.NewMasker()
		got, err := Submit([]string{"APP_NOTE=" + value, "APP_EMPTY=", "UNLISTED=" + value, "APP_TOKEN=" + value, "SPARKWING_RUN_ID=" + value}, []string{"APP_*", "MISSING", "SPARKWING_*"}, masker)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || string(got["APP_NOTE"]) != value || got["APP_EMPTY"] == nil {
			t.Fatal("submitted values escaped their allow-list or lost presence")
		}
		if masker.Mask(value) == value {
			t.Fatal("submitted value was not masked")
		}
	})
}

func TestSubmittedEnvironmentPreservesNativeBytes(t *testing.T) {
	value := string([]byte{255, 254, 'x'})
	got, err := Submit([]string{"EXACT=" + value}, []string{"EXACT"}, secrets.NewMasker())
	if err != nil || string(got["EXACT"]) != value {
		t.Fatal("submitted native bytes changed", err)
	}
	if _, err := Submit(nil, []string{"*"}, secrets.NewMasker()); err == nil {
		t.Fatal("invalid allow-list accepted")
	}
}

func TestSubmittedEnvironmentRejectsDuplicateProcessNames(t *testing.T) {
	if _, err := Submit([]string{"BUILD_NOTE=first", "BUILD_NOTE=second"}, []string{"BUILD_*"}, secrets.NewMasker()); err == nil {
		t.Fatal("duplicate submitted assignments accepted")
	}
	if runtime.GOOS == "windows" {
		if _, err := WithSubmitted(Base{Masker: secrets.NewMasker()}, map[string][]byte{"BUILD_NOTE": []byte("first"), "build_note": []byte("second")}, SubmitAcceptAll()); err == nil {
			t.Fatal("case-insensitive duplicate submitted assignments accepted")
		}
	}
}

// Property: for any operator allow-list and any submitted names, a submitted value reaches a
// remote pipeline process only under a name the operator lists, and never under a Go-tool or
// remote-base name, whatever the requester's own allow-list said.
func TestSubmittedNamesSurviveOnlyInsideTheOperatorAllowList(t *testing.T) {
	names := []string{"BUILD_REGION", "BUILD_NOTE", "APP_COLOR", "DEPLOY_TARGET", "SPARKWING_HOME", "GOFLAGS", "GOPROXY", "GONOSUMDB", "GOTOOLCHAIN", "GOMODCACHE", "GOCACHE", "PATH", "HOME", "HTTP_PROXY", "HTTPS_PROXY", "SSL_CERT_FILE", "TMPDIR", "LC_ALL"}
	patterns := []string{"BUILD_*", "BUILD_REGION", "APP_*", "DEPLOY_TARGET", "GOFLAGS", "GOPROXY", "GOMODCACHE", "PATH", "HTTP_*", "SPARKWING_HOME", "LC_*"}
	random := rand.New(rand.NewPCG(1, 2))
	for iteration := range 2000 {
		var operator []string
		for _, pattern := range patterns {
			if random.IntN(3) == 0 {
				operator = append(operator, pattern)
			}
		}
		accept, err := OperatorSubmitAccept(operator)
		if err != nil {
			t.Fatal(err)
		}
		submitted := map[string][]byte{}
		for _, name := range names {
			if random.IntN(2) == 0 {
				submitted[name] = []byte(fmt.Sprintf("submitted-%d-%s", iteration, name))
			}
		}
		base, err := WithSubmitted(Base{Ambient: []string{"PATH=/host/bin", "HOME=/host/home", "GOMODCACHE=/host/mod"}, SubmitEnvAllow: []string{"GOMODCACHE"}, Masker: secrets.NewMasker()}, submitted, accept)
		refused := slices.ContainsFunc(slices.Collect(maps.Keys(submitted)), func(name string) bool { return !submittable(name) })
		if refused != (err != nil) {
			t.Fatalf("operator %v submitted %v: refusal %t, error %v", operator, slices.Collect(maps.Keys(submitted)), refused, err)
		}
		if err != nil {
			continue
		}
		for _, kind := range []Kind{Install, Plan, Attempt, Eval} {
			environment, err := Build(kind, base, Runtime{})
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range environment {
				name, value, _ := strings.Cut(entry, "=")
				if !strings.HasPrefix(value, "submitted-") {
					continue
				}
				row, _ := Lookup(name)
				if kind == Install || !submitAllowed(name, operator) || row.GoTool || row.RemoteBase || !submittable(name) {
					t.Fatalf("operator %v: submitted %s reached a %d process", operator, name, kind)
				}
			}
		}
	}
}

func TestSubmittedGoToolchainAndNetworkNamesAreRefused(t *testing.T) {
	for _, name := range []string{"GOFLAGS", "GOPROXY", "GOTOOLCHAIN", "HTTP_PROXY", "PATH"} {
		if err := CheckSubmitted(map[string][]byte{name: []byte("value")}, secrets.NewMasker()); err == nil {
			t.Fatalf("submitted %s accepted", name)
		}
		got, err := Submit([]string{name + "=value"}, []string{name}, secrets.NewMasker())
		if err != nil || len(got) != 0 {
			t.Fatalf("requester captured %s: %v %v", name, got, err)
		}
	}
	accept, err := OperatorSubmitAccept(nil)
	if err != nil {
		t.Fatal(err)
	}
	base, err := WithSubmitted(Base{Masker: secrets.NewMasker()}, map[string][]byte{"BUILD_REGION": []byte("eu")}, accept)
	if err != nil || slices.Contains(base.SubmitEnvAllow, "BUILD_REGION") || len(base.Ambient) != 0 {
		t.Fatalf("an empty operator allow-list accepted a submitted name: %+v %v", base, err)
	}
}

func TestRemoteInstallsIgnoreTheHostGoEnvFile(t *testing.T) {
	remote, err := Build(Install, Base{Masker: secrets.NewMasker()}, Runtime{})
	if err != nil || !slices.Contains(remote, "GOENV=off") {
		t.Fatalf("remote install environment %v: %v", remote, err)
	}
	for _, base := range []Base{{Local: true}, {OwnerMachine: true, Masker: secrets.NewMasker()}} {
		owned, err := Build(Install, base, Runtime{})
		if err != nil || slices.Contains(owned, "GOENV=off") {
			t.Fatalf("an owner's install lost its go env file: %v %v", owned, err)
		}
	}
}

func TestSubmittedValuesNeverRedirectTheRunnersOwnState(t *testing.T) {
	for _, name := range []string{"SPARKWING_HOME", "SPARKWING_CONFIG", "GOMODCACHE", "GOCACHE", "GOWORK", "GOENV", "npm_config_cache", "DOCKER_HOST"} {
		if err := CheckSubmitted(map[string][]byte{name: []byte("/attacker")}, secrets.NewMasker()); err == nil {
			t.Errorf("submitted %s accepted", name)
		}
	}
	accept, err := OperatorSubmitAccept([]string{"BUILD_*"})
	if err != nil {
		t.Fatal(err)
	}
	base, err := WithSubmitted(Base{Ambient: []string{"BUILD_REGION=host"}, SubmitEnvAllow: []string{"BUILD_REGION"}, Masker: secrets.NewMasker()}, map[string][]byte{"BUILD_REGION": []byte("requester"), "BUILD_NOTE": []byte("requester-note")}, accept)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []Kind{Install, Plan, Attempt, Eval} {
		env, err := Build(kind, base, Runtime{})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(env, "BUILD_REGION=host") {
			t.Errorf("%d: a submitted value replaced the host's own: %v", kind, env)
		}
		if slices.Contains(env, "BUILD_NOTE=requester-note") == (kind == Install) {
			t.Errorf("%d: submitted delivery %v", kind, env)
		}
	}
}
