package store

import (
	"context"
)

// defaultFreeAllowanceBytes is the retained bytes a team stores without paying when the operator has not written
// storage_free_allowance_bytes. Billing, admission and the console all read it through storageFreeAllowanceTx.
const defaultFreeAllowanceBytes int64 = 1 << 30

// TeamStorageTier is whether a team stores on its funds or within the free allowance.
type TeamStorageTier string

const (
	// teamTierFunded has credits and no hold, or is the operator's own team.
	teamTierFunded TeamStorageTier = "funded"
	// teamTierFree stores within the free allowance.
	teamTierFree TeamStorageTier = "free"
)

// StorageStanding reports this team's funding, free allowance, retained bytes and live upload holds.
type StorageStanding struct {
	Tier              TeamStorageTier
	AllowanceBytes    int64
	EventBytes        int64
	BlobBytes         int64
	ReservedBlobBytes int64
	LogBytes          int64
}

// safety: the operator's team is the whole of a self-hosted install, which
// never had a free tier and must not gain its limits.
func holdsFreeAllowance(team Team) bool {
	team = NormalizeTeam(team)
	return team != "" && team != DefaultTeam
}

func storageStandingTx(ctx context.Context, tx *storeTx, team Team) (StorageStanding, error) {
	out := StorageStanding{Tier: teamTierFunded}
	var err error
	out.EventBytes, err = retainedEventBytesTx(ctx, tx, team)
	if err != nil {
		return StorageStanding{}, err
	}
	if tx.store.creditMeteringOff.Load() {
		return out, nil
	}
	if out.AllowanceBytes, err = storageFreeAllowanceTx(ctx, tx); err != nil {
		return StorageStanding{}, err
	}
	if !holdsFreeAllowance(team) {
		return out, nil
	}
	funded, err := teamFundedTx(ctx, tx, team)
	if err != nil {
		return StorageStanding{}, err
	}
	if !funded {
		out.Tier = teamTierFree
	}
	return out, nil
}

func teamFundedTx(ctx context.Context, tx *storeTx, team Team) (bool, error) {
	// safety: a held team, revoked standing included, may not keep what its
	// payments bought, so it stores and runs as an unfunded team does.
	balance, err := creditBalanceTx(ctx, tx, team)
	if err != nil || balance <= 0 {
		return false, err
	}
	cause, err := teamHoldTx(ctx, tx, team)
	return cause == "", err
}
