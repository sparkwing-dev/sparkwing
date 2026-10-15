package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/tests/acceptance/testdata/protocolgo"
)

func main() {
	if len(os.Args) == 3 && os.Args[1] == protocolgo.ChildHandlesArg {
		observation, err := protocolgo.ObserveChildHandles(os.Args[2])
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(observation)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 3 && os.Args[1] == protocolgo.ReadEnvArg {
		fmt.Println(os.Getenv(os.Args[2]))
		return
	}
	protocolgo.Register()
	sparkwing.Main()
}
