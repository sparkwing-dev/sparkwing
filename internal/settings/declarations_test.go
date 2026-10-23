package settings

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestDeclarationsRejectUnsetUnsupportedPayloads(t *testing.T) {
	for _, mutate := range []func(*runspec.CheckoutLayer){
		func(l *runspec.CheckoutLayer) { l.Profile.Value = "secret-value" },
		func(l *runspec.CheckoutLayer) { l.Requires.Value = []string{"secret-value"} },
		func(l *runspec.CheckoutLayer) { l.Guards.Origin = runspec.SettingOrigin{Layer: runspec.LayerFlag} },
	} {
		d := runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion, File: ".sparkwing/sparkwing.yaml"}
		mutate(&d.Defaults)
		err := validateCheckoutDeclarations(d)
		if err == nil || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("bad malformed-absence refusal: %v", err)
		}
	}
}

func TestDeclarationsRejectForgedNestedLocations(t *testing.T) {
	for _, mutate := range []func(*runspec.CheckoutDeclarations){
		func(d *runspec.CheckoutDeclarations) { d.Defaults.Args.Origin.Line = 0 },
		func(d *runspec.CheckoutDeclarations) { d.Defaults.Args.Origin.Key = "defaults.requires" },
		func(d *runspec.CheckoutDeclarations) {
			arg := d.Defaults.Args.Value["mode"]
			arg.Origin.Key = "defaults.args.other"
			d.Defaults.Args.Value["mode"] = arg
		},
		func(d *runspec.CheckoutDeclarations) {
			arg := d.Defaults.Args.Value["mode"]
			arg.Origin.File = "another.yaml"
			d.Defaults.Args.Value["mode"] = arg
		},
		func(d *runspec.CheckoutDeclarations) { d.Pipeline.Profile.Origin.Key = "pipelines[99].profile" },
	} {
		d, err := ProjectCheckoutDeclarations([]byte("defaults: {args: {mode: release}}\npipelines: [{name: build, entrypoint: Build, profile: prod, requires: []}]"), ".sparkwing/sparkwing.yaml", "build")
		if err != nil {
			t.Fatal(err)
		}
		mutate(&d)
		if err := validateCheckoutDeclarations(d); err == nil {
			t.Fatal("accepted forged location")
		}
	}
}

func TestDeclarationsProjectAliasesMergesAndEmptyValues(t *testing.T) {
	raw := []byte("defaults: &base\n  args: {mode: release}\n  requires: []\n  guards: {}\npipelines:\n  - name: build\n    entrypoint: Build\n    <<: *base\n    prefers: []\n")
	d, err := ProjectCheckoutDeclarations(raw, ".sparkwing/sparkwing.yaml", "build")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Pipeline.Args.Set || d.Pipeline.Args.Origin.Line != 2 || d.Pipeline.Args.Value["mode"].Origin.Line != 2 || d.Pipeline.Args.Value["mode"].Origin.Key != "pipelines[0].args.mode" || !d.Pipeline.Requires.Set || d.Pipeline.Requires.Value == nil || !d.Pipeline.Guards.Set || d.Pipeline.Prefers.Value == nil {
		t.Fatalf("lost values or origins: %+v", d)
	}
	d, err = ProjectCheckoutDeclarations([]byte("<<: {defaults: {args: {}}}"), ".sparkwing/sparkwing.yaml", "missing")
	if err != nil || !d.Defaults.Args.Set || d.Defaults.Args.Value == nil {
		t.Fatalf("top-level merge: %+v %v", d, err)
	}
}
