package settings

import (
	"fmt"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func declarationOrigin(source Source, file, key string, node *yaml.Node) runspec.SettingOrigin {
	line := 0
	if node != nil {
		line = node.Line
	}
	return runspec.SettingOrigin{Layer: originLayers[source], File: file, Key: key, Line: line}
}

func declaration[T any](value T, origin runspec.SettingOrigin) runspec.Setting[T] {
	return runspec.Setting[T]{Optional: runspec.Optional[T]{Set: true, Value: value}, Origin: origin}
}

func checkoutLayer(value projectconfig.Defaults, node *yaml.Node, source Source, file, prefix string) runspec.CheckoutLayer {
	out := runspec.CheckoutLayer{}
	origin := func(key string) runspec.SettingOrigin {
		return declarationOrigin(source, file, prefix+key, userconfig.YAMLField(node, key))
	}
	if value.Args != nil {
		args := map[string]runspec.Setting[string]{}
		for key, v := range value.Args {
			args[key] = declaration(v, declarationOrigin(source, file, prefix+"args."+key, userconfig.YAMLField(userconfig.YAMLField(node, "args"), key)))
		}
		out.Args = declaration(args, origin("args"))
	}
	if value.Guards != nil {
		out.Guards = declaration(runspec.GuardDeclarations{When: value.Guards.When, Unless: value.Guards.Unless}, origin("guards"))
	}
	if value.Requires != nil {
		out.Requires = declaration(value.Requires, origin("requires"))
	}
	if value.Prefers != nil {
		out.Prefers = declaration(value.Prefers, origin("prefers"))
	}
	return out
}

// ProjectCheckoutDeclarations validates the project schema and captures the selected pipeline
// and defaults without merging them. Raw file positions become file/key/line origins; no
// startup or credential field can enter the result. Missing files yield the versioned empty layers.
func ProjectCheckoutDeclarations(raw []byte, file, pipeline string) (runspec.CheckoutDeclarations, error) {
	out := runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion, File: file}
	if len(raw) == 0 {
		return out, validateCheckoutDeclarations(out)
	}
	cfg, err := projectconfig.Parse(raw)
	if err != nil {
		return out, fmt.Errorf("%s: %w", file, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return out, fmt.Errorf("%s: invalid YAML", file)
	}
	if len(doc.Content) == 0 {
		return out, validateCheckoutDeclarations(out)
	}
	root := doc.Content[0]
	if cfg.Defaults != nil {
		out.Defaults = checkoutLayer(*cfg.Defaults, userconfig.YAMLField(root, "defaults"), projectDefaults, file, "defaults.")
	}
	for i, entry := range cfg.Pipelines {
		if entry.Name != pipeline {
			continue
		}
		var node *yaml.Node
		if list := userconfig.YAMLField(root, "pipelines"); list != nil && i < len(list.Content) {
			node = list.Content[i]
		}
		prefix := fmt.Sprintf("pipelines[%d].", i)
		out.Pipeline = checkoutLayer(projectconfig.Defaults{Args: entry.Args, Guards: entry.Guards, Requires: entry.Requires, Prefers: entry.Prefers}, node, projectPipeline, file, prefix)
		if entry.Profile != nil {
			out.Pipeline.Profile = declaration(*entry.Profile, declarationOrigin(projectPipeline, file, prefix+"profile", userconfig.YAMLField(node, "profile")))
		}
		break
	}
	return out, validateCheckoutDeclarations(out)
}

func validateDeclarationOrigin(origin runspec.SettingOrigin, layer runspec.Layer, file string) error {
	if origin.Layer != layer || origin.File != file || origin.Key == "" || origin.Line <= 0 {
		return fmt.Errorf("checkout declaration origin does not match its structural source and file")
	}
	return nil
}

func validateDeclaration[T any](setting runspec.Setting[T], layer runspec.Layer, file string) error {
	if !setting.Set {
		if !reflect.DeepEqual(setting, runspec.Setting[T]{}) {
			return fmt.Errorf("absent checkout setting carries a value or origin")
		}
		return nil
	}
	return validateDeclarationOrigin(setting.Origin, layer, file)
}

func validateLayer(value runspec.CheckoutLayer, source Source, file string) error {
	layer := originLayers[source]
	if err := validateDeclaration(value.Args, layer, file); err != nil {
		return err
	}
	if err := validateDeclaration(value.Guards, layer, file); err != nil {
		return err
	}
	if err := validateDeclaration(value.Requires, layer, file); err != nil {
		return err
	}
	if err := validateDeclaration(value.Prefers, layer, file); err != nil {
		return err
	}
	if err := validateDeclaration(value.Profile, layer, file); err != nil {
		return err
	}
	prefix := ""
	for _, field := range []struct {
		key    string
		set    bool
		origin runspec.SettingOrigin
	}{
		{"args", value.Args.Set, value.Args.Origin}, {"guards", value.Guards.Set, value.Guards.Origin}, {"requires", value.Requires.Set, value.Requires.Origin}, {"prefers", value.Prefers.Set, value.Prefers.Origin}, {"profile", value.Profile.Set, value.Profile.Origin},
	} {
		if field.set {
			if err := validateDeclarationKey(field.origin, source, field.key, &prefix); err != nil {
				return err
			}
		}
	}
	if value.Args.Set && value.Args.Value == nil {
		return fmt.Errorf("present checkout args must not be null")
	}
	for key, arg := range value.Args.Value {
		if arg.Origin.Key != value.Args.Origin.Key+"."+key {
			return fmt.Errorf("checkout argument origin names a different key")
		}
		if !arg.Set {
			return fmt.Errorf("checkout argument must be present")
		}
		if err := validateDeclaration(arg, layer, file); err != nil {
			return err
		}
	}
	if value.Requires.Set && value.Requires.Value == nil || value.Prefers.Set && value.Prefers.Value == nil {
		return fmt.Errorf("present checkout list must not be null")
	}
	if source == projectDefaults && value.Profile.Set {
		return fmt.Errorf("profile is not permitted in project defaults")
	}

	defaults := projectconfig.Defaults{}
	if value.Guards.Set {
		defaults.Guards = &projectconfig.Guards{When: value.Guards.Value.When, Unless: value.Guards.Value.Unless}
	}
	if value.Requires.Set {
		defaults.Requires = value.Requires.Value
	}
	if value.Prefers.Set {
		defaults.Prefers = value.Prefers.Value
	}
	if err := projectconfig.Validate(projectconfig.Config{Defaults: &defaults}); err != nil {
		return invalid("guards", fromOrigin(value.Guards.Origin), "invalid guard declaration")
	}
	return nil
}

var pipelineOriginPrefix = regexp.MustCompile(`^pipelines\[(0|[1-9][0-9]*)\]\.$`)

func validateDeclarationKey(origin runspec.SettingOrigin, source Source, key string, prefix *string) error {
	if !slices.Contains(ruleFor(key).Sources, source) {
		return fmt.Errorf("checkout field is not permitted by its source")
	}
	expected := "defaults."
	if source == projectPipeline {
		if !strings.HasSuffix(origin.Key, "."+key) {
			return fmt.Errorf("checkout origin names a different field")
		}
		expected = strings.TrimSuffix(origin.Key, key)
		if !pipelineOriginPrefix.MatchString(expected) {
			return fmt.Errorf("checkout origin must name a pipeline entry")
		}
	}
	if origin.Key != expected+key {
		return fmt.Errorf("checkout origin names a different field")
	}
	if *prefix != "" && *prefix != expected {
		return fmt.Errorf("checkout origins name different pipeline entries")
	}
	*prefix = expected
	return nil
}

// validateCheckoutDeclarations checks the format, presence and field/source membership in Table.
// Every nested Origin must agree with its structural layer and checkout-relative file. An origin
// claiming flag, env, run, machine or operator authority is invalid. Explicit empty/zero survives.
// Both acceptance and expansion call this before resolving any author-provided setting.
func validateCheckoutDeclarations(value runspec.CheckoutDeclarations) error {
	if value.Version != runspec.CheckoutDeclarationsVersion {
		return fmt.Errorf("unsupported checkout declarations version")
	}
	if value.File != "" && (value.File == "." || path.IsAbs(value.File) || path.Clean(value.File) != value.File || value.File == ".." || strings.HasPrefix(value.File, "../") || strings.ContainsAny(value.File, "\\\x00:")) {
		return fmt.Errorf("checkout declarations file must be checkout-relative")
	}
	if value.File == "" && (!reflect.DeepEqual(value.Pipeline, runspec.CheckoutLayer{}) || !reflect.DeepEqual(value.Defaults, runspec.CheckoutLayer{})) {
		return fmt.Errorf("checkout declarations require a file")
	}
	if err := validateLayer(value.Pipeline, projectPipeline, value.File); err != nil {
		return err
	}
	return validateLayer(value.Defaults, projectDefaults, value.File)
}
