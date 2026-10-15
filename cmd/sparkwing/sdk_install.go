package main

import (
	"errors"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/engine/sdkinstall"
)

func runSDKInstall(args []string) error {
	if len(args) != 0 {
		return errors.New("__sdk-install accepts its request only on stdin")
	}
	ctx, stop, _ := interruptContext()
	defer stop()
	return sdkinstall.Run(ctx, installedVersion(), os.Stdin, os.Stdout, os.Stderr)
}
