package main

import (
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/tests/acceptance/testdata/protocolgo"
)

func main() {
	protocolgo.Register()
	sparkwing.Main()
}
