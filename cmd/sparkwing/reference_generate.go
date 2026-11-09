//go:build ignore

package main

import (
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

// perf: explicit source files reuse the CLI's registry and renderer without linking the runtime.
func main() {
	inv, err := clisurface.Parse("sparkwing", append([]string{"commands"}, os.Args[1:]...))
	if err == nil {
		err = runCommands(inv)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
