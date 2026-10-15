package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// safety: the free tier counts teams, not bytes: a team without credits takes a slot on its first store, released only
// when the team is deleted, so free bytes never pass slots times the allowance. Each store holds a slotted team to its
// share where it writes (the controller's is run events). Runs are never refused here; a metered claim refuses them.

// DefaultFreeTeamSlots is how many teams without credits may hold a free-tier
// slot when the operator has set no other number.
const DefaultFreeTeamSlots = 200

// DefaultFreeAllowanceBytes is a free team's allowance across every store
// when the operator has not written storage_free_allowance_bytes.
const DefaultFreeAllowanceBytes int64 = 1 << 30

// TeamStorageTier is what a team may store.
type TeamStorageTier string

const (
	// TeamTierFunded stores without a byte limit: a team with credits,
	// or the operator's own team.
	TeamTierFunded TeamStorageTier = "funded"
	// TeamTierFree holds a free-tier slot and stores up to its shares.
	TeamTierFree TeamStorageTier = "free"
	// TeamTierNone has neither credits nor a slot and stores nothing.
	TeamTierNone TeamStorageTier = "none"
)

// FreeEventShare is the part of allowance a free team's run events are held
// to, one sixteenth; the cache and the node logs hold the rest.
func FreeEventShare(allowance int64) int64 { return allowance / 16 }

// StorageLimitFreeEvents names a free team's event share in a
// [StorageQuotaError].
const StorageLimitFreeEvents = "free_event_share"

const metaKeyFreeTeamSlots = "free_team_slots"

// ErrFreeStoragePaused refuses a team with neither credits nor a free-tier
// slot, because every slot is taken.
var ErrFreeStoragePaused = errors.New("free storage is paused; buy credits or join the waitlist")

// StorageStanding reports this team's policy, retained bytes and live upload holds.
type StorageStanding struct {
	Tier TeamStorageTier
	// AllowanceBytes is the whole free allowance the per-store shares are
	// cut from.
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
	out := StorageStanding{Tier: TeamTierFunded}
	var err error
	out.EventBytes, err = retainedEventBytesTx(ctx, tx, team)
	if err != nil {
		return StorageStanding{}, err
	}
	if creditMeteringDisabled(ctx) {
		return out, nil
	}
	allowance, err := creditSettingTx(ctx, tx, metaKeyStorageFreeAllowanceBytes, DefaultFreeAllowanceBytes)
	if err != nil {
		return StorageStanding{}, err
	}
	out.AllowanceBytes = max(allowance, 0)
	if !holdsFreeAllowance(team) {
		return out, nil
	}
	slotted, err := rowPresentTx(ctx, tx, `SELECT 1 FROM free_slots WHERE team=?`, team)
	if err != nil {
		return StorageStanding{}, err
	}
	funded, err := teamFundedTx(ctx, tx, team)
	if err != nil {
		return StorageStanding{}, err
	}
	switch {
	case funded:
	case slotted:
		out.Tier = TeamTierFree
	default:
		out.Tier = TeamTierNone
	}
	return out, nil
}

func teamFundedTx(ctx context.Context, tx *storeTx, team Team) (bool, error) {
	// safety: a card-billed team is funded by its card's credit, not only by
	// what it prepaid; a spent limit pauses its work but not its tier.
	st, err := spendStandingTx(ctx, tx, team, tx.now())
	if err != nil || st.BalanceMicro+st.CreditLimitMicro <= 0 {
		return false, err
	}
	// safety: a team held over a disputed payment may not keep what that
	// payment bought, so it stores and runs as an unfunded team does.
	freeze, err := teamCreditFreezeTx(ctx, tx, team)
	if err != nil {
		return false, err
	}
	return !freeze.Frozen, nil
}

// safety: Postgres runs concurrent writers in their own transactions, so
// the slot count serializes on one lock; SQLite allows one writer and is
// already serial.
func lockFreeTierTx(ctx context.Context, tx *storeTx) error {
	if tx.dialect != DialectPostgres {
		return nil
	}
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext(?))`, "sparkwing/free-tier")
	return err
}

func takeFreeSlotTx(ctx context.Context, tx *storeTx, team Team, now time.Time) (bool, error) {
	open, err := freeSlotOpenTx(ctx, tx)
	if err != nil {
		return false, err
	}
	if !open {
		return rowPresentTx(ctx, tx, `SELECT 1 FROM free_slots WHERE team = ?`, string(team))
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO free_slots (team, taken_at)
SELECT ?, ?
 WHERE NOT EXISTS (SELECT 1 FROM free_slots WHERE team = ?)`,
		string(team), now.UnixNano(), string(team)); err != nil {
		return false, err
	}
	return rowPresentTx(ctx, tx, `SELECT 1 FROM free_slots WHERE team = ?`, string(team))
}

// safety: a reservation only asks whether a slot is left; the team's first
// committed byte takes it, so an abandoned reservation spends none.
func freeSlotOpenTx(ctx context.Context, tx *storeTx) (bool, error) {
	limit, err := creditSettingTx(ctx, tx, metaKeyFreeTeamSlots, DefaultFreeTeamSlots)
	if err != nil {
		return false, err
	}
	var taken int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM free_slots`).Scan(&taken); err != nil {
		return false, err
	}
	return taken < limit, nil
}

func freeStoragePaused(team Team) error {
	return fmt.Errorf("%w: team %s has no credits and every free-tier slot is taken", ErrFreeStoragePaused, team)
}

// safety: a team with no slot takes one here, at its first stored byte.
// The slot row stays locked until the caller's transaction ends, so two appends cannot count the same room.
func admitFreeEventsTx(ctx context.Context, tx *storeTx, team Team, principal string, bytes int64, now time.Time) error {
	if creditMeteringDisabled(ctx) {
		return nil
	}
	if bytes <= 0 {
		return nil
	}
	funded, err := teamFundedTx(ctx, tx, team)
	if err != nil || funded {
		return err
	}
	slotted, err := rowPresentTx(ctx, tx, `SELECT 1 FROM free_slots WHERE team = ?`, string(team))
	if err != nil {
		return err
	}
	if !slotted {
		if err := lockFreeTierTx(ctx, tx); err != nil {
			return err
		}
		if slotted, err = takeFreeSlotTx(ctx, tx, team, now); err != nil {
			return err
		}
		if !slotted {
			return freeStoragePaused(team)
		}
	}
	if err := lockFreeTierTx(ctx, tx); err != nil {
		return err
	}
	used, err := retainedEventBytesTx(ctx, tx, team)
	if err != nil {
		return err
	}
	allowance, err := creditSettingTx(ctx, tx, metaKeyStorageFreeAllowanceBytes, DefaultFreeAllowanceBytes)
	if err != nil {
		return err
	}
	share := FreeEventShare(max(allowance, 0))
	if used+bytes > share {
		return &StorageQuotaError{
			Principal: principal, Limit: StorageLimitFreeEvents, Unit: "bytes",
			Used: used, Allowed: share, Requested: bytes,
			Remedy: "the team has no credits, so its run events keep at most their share of the free allowance; " +
				"add credits to store more, and runs past the retention window release theirs",
		}
	}
	return nil
}

// GrantFreeSlot gives team a free-tier slot whether or not one is free.
func (s *Store) GrantFreeSlot(ctx context.Context, team Team, now time.Time) (err error) {
	team = NormalizeTeam(team)
	if err := ValidateSlug(string(team)); err != nil {
		return err
	}
	if !holdsFreeAllowance(team) {
		return fmt.Errorf("%w: the operator's team has no free tier", ErrInvalidInput)
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	exists, err := rowPresentTx(ctx, tx, `SELECT 1 FROM teams WHERE name = ?`, string(team))
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: team %s", ErrNotFound, team)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO free_slots (team, taken_at) VALUES (?,?)
ON CONFLICT (team) DO NOTHING`, string(team), now.UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}

// SetFreeTeamSlots sets how many teams without credits may hold a slot.
// Lowering it takes no slot back.
func (s *Store) SetFreeTeamSlots(ctx context.Context, slots int64) error {
	if slots < 0 {
		return fmt.Errorf("%w: free team slots must not be negative", ErrInvalidInput)
	}
	return s.inTx(ctx, func(tx *storeTx) error {
		return setCreditSettingTx(ctx, tx, metaKeyFreeTeamSlots, formatCreditSetting(slots))
	})
}

// FreeSlots reports how many free-tier slots are taken and how many exist.
func (s *Store) FreeSlots(ctx context.Context) (taken, limit int64, err error) {
	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM free_slots`).Scan(&taken); err != nil {
		return 0, 0, err
	}
	limit, err = s.creditSetting(ctx, metaKeyFreeTeamSlots, DefaultFreeTeamSlots)
	return taken, limit, err
}
