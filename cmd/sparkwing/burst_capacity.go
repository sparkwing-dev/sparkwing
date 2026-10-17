package main

import (
	"context"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func observeBurstCapacity(ctx context.Context, home string) (match.Resources, match.Resources, error) {
	state, err := wingdclient.Query(ctx, wingdclient.Options{Home: home, Version: installedVersion()})
	if err != nil {
		return match.Resources{}, match.Resources{}, err
	}
	return wingdclient.Capacity(state)
}
