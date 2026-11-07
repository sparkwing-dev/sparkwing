package jobs

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func validateGateBase(base string) error {
	decoded, err := hex.DecodeString(base)
	if err != nil || len(decoded) != 20 {
		return errors.New("gate: --base must be a full commit SHA from this public repository")
	}
	return nil
}

func fetchGateBase(ctx context.Context, remote, base string) error {
	if err := validateGateBase(base); err != nil {
		return err
	}
	if _, err := sparkwing.Exec(ctx, "git", "fetch", "--no-tags", "--depth=1", remote, base+":refs/remotes/"+gateBaselineRef).Run(); err != nil {
		return fmt.Errorf("gate: fetch comparison commit %s from %s: %w", base, remote, err)
	}
	return nil
}
