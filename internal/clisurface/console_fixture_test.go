package clisurface

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var updateConsoleCommands = flag.Bool("update-console-commands", false, "rewrite the console's CLI flag fixture")

// TestConsoleCommandFlagsFixture keeps the flags the console's setup commands may use
// equal to what the parser accepts.
func TestConsoleCommandFlagsFixture(t *testing.T) {
	inventory := map[string][]string{}
	for _, path := range []string{"sparkwing profiles add", "sparkwing-runner warm"} {
		found := false
		for _, command := range Registry() {
			if command.Path != path {
				continue
			}
			found = true
			inventory[path] = []string{}
			for _, f := range append(GlobalFlags(), command.Flags...) {
				inventory[path] = append(inventory[path], f.Name)
			}
		}
		if !found {
			t.Fatalf("registry has no %q", path)
		}
	}
	want, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	path := filepath.Join("..", "..", "web", "src", "lib", "testdata", "cli-flags.json")
	if *updateConsoleCommands {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var recorded map[string][]string
	if err := json.Unmarshal(got, &recorded); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if !reflect.DeepEqual(recorded, inventory) {
		t.Errorf("%s differs from the CLI registry; rerun with -update-console-commands and check the console commands", path)
	}
}
