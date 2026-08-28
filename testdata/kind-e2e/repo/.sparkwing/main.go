package main

import (
	_ "sparkwing-kind-e2e-pipelines/jobs"

	"github.com/sparkwing-dev/sparkwing/pkg/runner"
)

func main() {
	runner.Main()
}
