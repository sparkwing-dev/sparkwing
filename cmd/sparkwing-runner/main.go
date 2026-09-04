package main

import (
	"github.com/sparkwing-dev/sparkwing/internal/cluster"
)

// Version is stamped by the release build.
var Version string

func main() {
	cluster.MainWithVersion(Version)
}
