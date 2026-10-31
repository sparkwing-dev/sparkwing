package main

import (
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testleak"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "__sdk-install" {
		main()
		os.Exit(0)
	}
	testleak.Main(m)
}
