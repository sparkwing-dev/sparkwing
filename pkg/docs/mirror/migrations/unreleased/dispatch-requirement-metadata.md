## Dispatch requirement metadata

`pipelines.Pipeline.Requires` is removed. Dispatch metadata describes pipeline identity and triggers. The host resolves execution requirements from project declarations and request overrides. YAML `defaults.requires` and pipeline `requires` keep their syntax and precedence.

Code inspecting raw declarations can use the parsed project:

```go
// Before
requires := cfg.Registry().Find("build").Requires

// After: inspect declarations, preserving unset versus explicit empty.
defaults := cfg.Defaults
pipelines := cfg.Pipelines
```

Do not derive a planning claim from registry membership. SDK pipelines may have no YAML entry.
