package main

import (
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/tests/acceptance/testdata/protocolgo"
)

func main() {
	if len(os.Args) == 3 && os.Args[1] == protocolgo.ReadEnvArg {
		fmt.Println(os.Getenv(os.Args[2]))
		return
	}
	protocolgo.Register()
	sparkwing.Main()
}
