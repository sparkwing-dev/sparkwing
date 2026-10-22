package nodehost

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestClaimedEnvironmentRegistrationCannotCollideWithAcceptedIdentifiers(t *testing.T) {
	property := func(seed [16]byte) bool {
		value := "private-" + hex.EncodeToString(seed[:])
		config, input, state, _ := nativeHostFixture(t)
		switch seed[0] % 3 {
		case 0:
			input.Accepted.Plan.Jobs[0].Caches = []nodeproto.CacheSpec{{Name: "cache-" + value}}
		case 1:
			input.Accepted.Plan.Jobs[0].References = []nodeproto.PipelineReference{{Pipeline: "pipeline-" + value, Job: "build"}}
		case 2:
			input.Accepted.Plan.Jobs[0].References = []nodeproto.PipelineReference{{Pipeline: "build", Job: "job-" + value}}
		}
		state.run.Plan, _ = json.Marshal(input.Accepted.Plan)
		config.Source.Base = procenv.Base{Ambient: []string{"APP_VALUE=" + value}, SubmitEnvAllow: []string{"APP_VALUE"}}
		_, claimed, _, err := NewNativeHost(config)
		if err != nil {
			t.Fatal(err)
		}
		before := config.Masks.masker.Values()
		called := false
		claimed.Acquire = func(_ context.Context, _ source.Spec, options source.Options) (*source.Entry, error) {
			called = true
			env, err := options.BuildEnvironment(procenv.Install, options.Base, procenv.Runtime{})
			if len(slices.Collect(env.Values())) != 0 {
				t.Fatal("colliding environment was projected")
			}
			return nil, err
		}
		err = claimed.Execute(t.Context(), input)
		var structured *apierr.Error
		if !called || !errors.As(err, &structured) || structured.Code != apierr.SecretInIdentifier || strings.Contains(err.Error(), value) {
			t.Fatalf("environment collision not refused safely: %v", err)
		}
		if !reflect.DeepEqual(before, config.Masks.masker.Values()) {
			t.Fatal("refused environment changed run masks")
		}
		return !t.Failed()
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 16}); err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentRegistrationPublishesOnlyAcceptedPatterns(t *testing.T) {
	registry := NewMaskRegistry(secrets.NewMasker())
	value := "private-proxy-password"
	build := registry.environment(nil)
	env, err := build(procenv.Plan, procenv.Base{Local: true, Captured: []string{"HTTPS_PROXY=https://maskuser:" + value + "@proxy"}}, procenv.Runtime{})
	if err != nil || len(slices.Collect(env.Values())) == 0 || registry.masker.Mask(value) != "***" {
		t.Fatalf("environment registration lost masking: %v", err)
	}
	expected := secrets.NewMasker()
	if _, err := procenv.Build(procenv.Plan, procenv.Base{Local: true, Captured: slices.Collect(env.Values()), Masker: expected}, procenv.Runtime{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(expected.Values(), registry.masker.Values()) {
		t.Fatal("projection changed policy masking patterns")
	}
}
