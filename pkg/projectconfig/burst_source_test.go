package projectconfig

import "testing"

func TestPipelineDeclaresUntrackedBurstSourceFiles(t *testing.T) {
	cfg, err := Parse([]byte("pipelines:\n  - name: test\n    entrypoint: main.go\n    source_files: [generated/input.json, ignored-fixture.txt]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Pipelines[0].SourceFiles) != 2 || cfg.Pipelines[0].SourceFiles[0] != "generated/input.json" {
		t.Fatalf("source file declarations lost: %+v", cfg.Pipelines[0])
	}
}

func TestPipelineRejectsUnsafeBurstSourceFiles(t *testing.T) {
	for _, path := range []string{"../outside", "/etc/passwd", ".git/config", "nested/.GIT/config", ":(glob)*", "a/../secret", "a\\secret", ""} {
		if _, err := Parse([]byte("pipelines:\n  - name: test\n    entrypoint: main.go\n    source_files: ['" + path + "']\n")); err == nil {
			t.Fatalf("accepted unsafe source file %q", path)
		}
	}
}
