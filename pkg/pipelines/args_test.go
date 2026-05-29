package pipelines

import (
	"strings"
	"testing"
)

func TestEffectiveTargets_PrefersArgsTargetOverLegacy(t *testing.T) {
	// Legacy-only.
	p := &Pipeline{Name: "p", Targets: map[string]Target{"dev": {}}}
	if got := p.EffectiveTargets(); len(got) != 1 || got["dev"].Runners != nil {
		t.Errorf("legacy targets should fall through; got %+v", got)
	}

	// args.target wins when both present (which ValidateArgs would
	// have rejected, but EffectiveTargets is permissive on its own).
	p = &Pipeline{
		Name:    "p",
		Targets: map[string]Target{"dev": {}},
		Args: PipelineArgs{
			"target": map[string]Target{
				"prod": {Runners: []string{"my-pool"}},
			},
		},
	}
	got := p.EffectiveTargets()
	if _, ok := got["dev"]; ok {
		t.Error("EffectiveTargets should NOT include legacy keys when args.target is set")
	}
	if _, ok := got["prod"]; !ok {
		t.Error("EffectiveTargets should include args.target keys")
	}

	// Nothing -> nil/empty.
	p = &Pipeline{Name: "p"}
	if got := p.EffectiveTargets(); got != nil {
		t.Errorf("empty targets should be nil; got %v", got)
	}
}

func TestArgsTarget_Accessor(t *testing.T) {
	a := PipelineArgs{"target": map[string]Target{"prod": {}}}
	if got := a.Target(); len(got) != 1 {
		t.Errorf("Target() should return inner map; got %v", got)
	}
	var nilArgs PipelineArgs
	if got := nilArgs.Target(); got != nil {
		t.Errorf("nil Args.Target() should be nil; got %v", got)
	}
	a = PipelineArgs{"otherkey": map[string]Target{}}
	if got := a.Target(); got != nil {
		t.Errorf("Args without target key should return nil; got %v", got)
	}
}

func TestValidateArgs_RejectsBothLegacyAndNew(t *testing.T) {
	p := &Pipeline{
		Name:    "release",
		Targets: map[string]Target{"dev": {}},
		Args:    PipelineArgs{"target": map[string]Target{"prod": {}}},
	}
	err := p.ValidateArgs()
	if err == nil || !strings.Contains(err.Error(), "release") ||
		!strings.Contains(err.Error(), "args.target") {
		t.Fatalf("expected error naming the pipeline and both shapes; got %v", err)
	}
}

func TestValidateArgs_RejectsUnknownBindName(t *testing.T) {
	p := &Pipeline{
		Name: "release",
		Args: PipelineArgs{
			"runner": map[string]Target{"my-pool": {}}, // future bind, not yet supported
		},
	}
	err := p.ValidateArgs()
	if err == nil || !strings.Contains(err.Error(), "runner") ||
		!strings.Contains(err.Error(), "v0.6") {
		t.Fatalf("expected error naming the unsupported bind name and v0.6 constraint; got %v", err)
	}
}

func TestValidateArgs_AcceptsTargetOnly(t *testing.T) {
	p := &Pipeline{
		Name: "release",
		Args: PipelineArgs{"target": map[string]Target{"prod": {Runners: []string{"my-pool"}}}},
	}
	if err := p.ValidateArgs(); err != nil {
		t.Errorf("args.target only should pass; got %v", err)
	}
}

func TestValidateArgs_AcceptsLegacyTargetsOnly(t *testing.T) {
	p := &Pipeline{
		Name:    "release",
		Targets: map[string]Target{"dev": {}, "prod": {}},
	}
	if err := p.ValidateArgs(); err != nil {
		t.Errorf("legacy targets only should pass; got %v", err)
	}
}

func TestValidateArgs_AcceptsNeither(t *testing.T) {
	p := &Pipeline{Name: "release"}
	if err := p.ValidateArgs(); err != nil {
		t.Errorf("no targets and no args should pass; got %v", err)
	}
}

func TestValidateArgs_NilPipelineSafe(t *testing.T) {
	var p *Pipeline
	if err := p.ValidateArgs(); err != nil {
		t.Errorf("nil pipeline should not error; got %v", err)
	}
}

func TestHasLegacyTargets(t *testing.T) {
	if (&Pipeline{Targets: map[string]Target{"dev": {}}}).HasLegacyTargets() != true {
		t.Error("pipeline with only legacy Targets should report HasLegacyTargets=true")
	}
	if (&Pipeline{Args: PipelineArgs{"target": map[string]Target{"prod": {}}}}).HasLegacyTargets() != false {
		t.Error("pipeline with args.target should report HasLegacyTargets=false")
	}
	if (&Pipeline{}).HasLegacyTargets() != false {
		t.Error("empty pipeline should report HasLegacyTargets=false")
	}
}
