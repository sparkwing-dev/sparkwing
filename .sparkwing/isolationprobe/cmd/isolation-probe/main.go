// Command isolation-probe is the cloud-isolation fixture's pipeline binary. The driver copies this
// file to the fixture's .sparkwing/main.go. Given the isolation-probe argument it runs the probe
// instead of the pipeline, which is how a DinD container starts the same static executable.
package main

import (
	"os"
	"sparkwing-pipelines/isolationprobe"
	"sparkwing-pipelines/isolationprobe/fixture"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == isolationprobe.Command {
		os.Exit(isolationprobe.Main(os.Args[2:], os.Stdout, os.Stderr))
	}
	fixture.Register()
	sparkwing.Main()
}
