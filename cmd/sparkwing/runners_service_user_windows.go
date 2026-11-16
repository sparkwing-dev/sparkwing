package main

import (
	"context"
	"errors"

	runnerservice "github.com/sparkwing-dev/sparkwing/internal/runners/service"
)

func runAsServiceAccount(context.Context, runnerservice.Account, []string, []byte) ([]byte, error) {
	return nil, errors.New("a dedicated runner account is supported on Linux and macOS")
}
