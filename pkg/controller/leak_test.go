package controller_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine/sdkinstall"
	"github.com/sparkwing-dev/sparkwing/internal/testleak"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "__sdk-install" {
		if err := sdkinstall.Run(context.Background(), "development", os.Stdin, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	testleak.Main(m)
}
