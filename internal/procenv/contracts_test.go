package procenv

import "testing"

func TestRegistryClassifiesCredentialAndRuntimeBoundaries(t *testing.T) {
	seen := map[string]Var{}
	for _, row := range Registry {
		if _, ok := seen[row.Name]; ok {
			t.Errorf("duplicate registry name %s", row.Name)
		}
		seen[row.Name] = row
		if row.Name == "" || row.Owner == "" || row.Doc == "" {
			t.Errorf("incomplete registry row: %+v", row)
		}
	}
	for _, name := range []string{"SPARKWING_NODE_TOKEN", "SPARKWING_AGENT_TOKEN", "SPARKWING_CACHE_GRANT", "SPARKWING_SOURCE_TOKEN", "SPARKWING_SECRETS_KEY"} {
		row, ok := seen[name]
		if !ok || row.Class != Credential || row.Owner != "engine" {
			t.Errorf("credential escapes classification: %s", name)
		}
	}
	for _, name := range []string{"SPARKWING_JOB_ID", "SPARKWING_RUN_ID", "CI", "SPARKWING_COMMIT", "SPARKWING_TRIGGER_SOURCE"} {
		row, ok := seen[name]
		if !ok || row.Class != RuntimeClass || row.Owner != "engine" {
			t.Errorf("runtime name lacks authority: %s", name)
		}
	}
}
