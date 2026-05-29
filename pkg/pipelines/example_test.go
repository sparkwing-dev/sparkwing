package pipelines_test

import (
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

// ExampleParse loads a small pipelines.yaml document and walks the
// registered pipeline names. Use [pipelines.Load] to read from disk
// (any consumer wiring against a real .sparkwing/ tree would).
func ExampleParse() {
	yaml := `
pipelines:
  - name: lint
    entrypoint: Lint
  - name: build
    entrypoint: Build
`
	cfg, err := pipelines.Parse(strings.NewReader(yaml))
	if err != nil {
		fmt.Println("parse:", err)
		return
	}
	if err := cfg.Validate(); err != nil {
		fmt.Println("validate:", err)
		return
	}
	for _, p := range cfg.Pipelines {
		fmt.Printf("%s -> %s\n", p.Name, p.Entrypoint)
	}
	// Output:
	// lint -> Lint
	// build -> Build
}
