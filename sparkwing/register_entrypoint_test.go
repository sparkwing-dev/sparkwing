package sparkwing_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type entrypointArgs struct {
	Replicas int `flag:"replicas" desc:"replicas"`
}

type entrypointPipe struct{ sparkwing.Base }

func (entrypointPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ entrypointArgs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "noop", func(_ context.Context) error { return nil })
	return nil
}

func TestRegisterEntrypoint_NotLookupableUntilBound(t *testing.T) {
	sparkwing.RegisterEntrypoint[entrypointArgs]("EntrypointTestPipe", func() sparkwing.Pipeline[entrypointArgs] {
		return entrypointPipe{}
	})
	if _, ok := sparkwing.Lookup("EntrypointTestPipe"); ok {
		t.Error("RegisterEntrypoint alone should not be Lookup-able; YAML binding required")
	}
}

type yamlCfgStub struct {
	pairs []struct{ name, entrypoint string }
}

func (s yamlCfgStub) EachPipeline(fn func(name, entrypoint string)) {
	for _, p := range s.pairs {
		fn(p.name, p.entrypoint)
	}
}

func TestBindPipelinesFromYAML_BindsEntrypointUnderPipelineNames(t *testing.T) {
	sparkwing.RegisterEntrypoint[entrypointArgs]("BindFixture", func() sparkwing.Pipeline[entrypointArgs] {
		return entrypointPipe{}
	})

	sparkwing.BindPipelinesFromYAML(yamlCfgStub{
		pairs: []struct{ name, entrypoint string }{
			{"bind-prod", "BindFixture"},
			{"bind-dev", "BindFixture"},
		},
	})

	for _, n := range []string{"bind-prod", "bind-dev"} {
		if reg, ok := sparkwing.Lookup(n); !ok {
			t.Errorf("Lookup(%q) failed; binding did not register the pipeline name", n)
		} else if reg.Name != n {
			t.Errorf("Lookup(%q).Name = %q, want %q (binding should rename to pipeline-side identifier)", n, reg.Name, n)
		}
	}
}

func TestBindPipelinesFromYAML_PreservesExistingRegistration(t *testing.T) {
	sparkwing.Register[entrypointArgs]("preexisting-bind", func() sparkwing.Pipeline[entrypointArgs] {
		return entrypointPipe{}
	})
	before, _ := sparkwing.Lookup("preexisting-bind")

	sparkwing.BindPipelinesFromYAML(yamlCfgStub{
		pairs: []struct{ name, entrypoint string }{
			{"preexisting-bind", "EntrypointTestPipe"},
		},
	})

	after, _ := sparkwing.Lookup("preexisting-bind")
	if before != after {
		t.Error("BindPipelinesFromYAML should preserve a pre-existing Register binding")
	}
}

func TestBindPipelinesFromYAML_SkipsUnregisteredEntrypoints(t *testing.T) {
	sparkwing.BindPipelinesFromYAML(yamlCfgStub{
		pairs: []struct{ name, entrypoint string }{
			{"orphan-pipeline", "NeverRegistered"},
		},
	})
	if _, ok := sparkwing.Lookup("orphan-pipeline"); ok {
		t.Error("YAML pipeline naming an unregistered entrypoint should not Lookup")
	}
}
