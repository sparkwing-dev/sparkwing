package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/sparks"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func resolveSparks(ctx context.Context, sparkwingDir string, noUpdate bool) error {
	if noUpdate {
		return nil
	}
	m, err := projectconfig.LoadSparksManifest(sparkwingDir)
	if err != nil {
		return fmt.Errorf("sparks resolve: %w", err)
	}
	if _, err := sparks.ResolveAndWrite(ctx, sparkwingDir, m); err != nil {
		var toolchainErr *gotoolchain.Error
		if errors.As(err, &toolchainErr) {
			return fmt.Errorf("sparks resolve: %w", err)
		}
		if explanation := gotoolchain.ExplainOutput(ctx, err.Error(), nil); explanation != nil {
			return fmt.Errorf("sparks resolve: %w", explanation)
		}
		return fmt.Errorf("sparks resolve: %w (use --sw-no-update to compile against existing go.mod pins)", err)
	}
	return nil
}
