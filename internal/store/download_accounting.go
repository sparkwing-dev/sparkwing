package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// DownloadCharge counts signed bytes or delivered bytes under the controller's replica identity.
type DownloadCharge struct {
	ReplicaID                    string
	Bytes                        int64
	Record                       bool
	FreeCapBytes, FundedCapBytes int64
}

// DownloadCharged reports this team's shared daily admission total.
type DownloadCharged struct {
	Tier     TeamStorageTier `json:"tier"`
	DayBytes int64           `json:"day_bytes"`
	CapBytes int64           `json:"cap_bytes"`
}

// ErrDownloadCap matches a refusal at the team's daily download cap.
var ErrDownloadCap = errors.New("daily download cap reached")

// DownloadCapError reports the shared usage and the time until midnight UTC.
type DownloadCapError struct {
	Team           string
	Day            string
	UsedBytes      int64
	CapBytes       int64
	RequestedBytes int64
	RetryAfter     time.Duration
	Remedy         string
}

func (e *DownloadCapError) Error() string {
	return fmt.Sprintf("%s: team %s downloaded %d of its %d bytes on %s and this download is %d; %s",
		ErrDownloadCap, e.Team, e.UsedBytes, e.CapBytes, e.Day, e.RequestedBytes, e.Remedy)
}

// Unwrap reports ErrDownloadCap.
func (e *DownloadCapError) Unwrap() error { return ErrDownloadCap }

// ChargeDownload admits under the shared quota lock and records additive replica usage atomically.
func (t *Tenant) ChargeDownload(ctx context.Context, c DownloadCharge) (_ DownloadCharged, err error) {
	if c.ReplicaID == "" || c.Bytes < 0 || c.FreeCapBytes < 0 || c.FundedCapBytes < 0 {
		return DownloadCharged{}, fmt.Errorf("%w: invalid download charge", ErrInvalidInput)
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return DownloadCharged{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	out, err := t.chargeDownloadTx(ctx, tx, c)
	if err != nil {
		return DownloadCharged{}, err
	}
	return out, tx.Commit()
}

func (t *Tenant) chargeDownloadTx(ctx context.Context, tx *storeTx, c DownloadCharge) (DownloadCharged, error) {
	if c.ReplicaID == "" || c.Bytes < 0 || c.FreeCapBytes < 0 || c.FundedCapBytes < 0 {
		return DownloadCharged{}, fmt.Errorf("%w: invalid download charge", ErrInvalidInput)
	}
	now, clockErr1 := tx.now(ctx)
	if clockErr1 != nil {
		return DownloadCharged{}, clockErr1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO quota_admission(team,quota,period,updated_at) VALUES (?,'download',?,?) ON CONFLICT(team,quota) DO NOTHING`, t.team, now.Format("2006-01-02"), now.UnixNano()); err != nil {
		return DownloadCharged{}, err
	}
	var prior string
	var used, reserved int64
	if err := tx.QueryRowContext(ctx, "SELECT period,used FROM quota_admission WHERE team=? AND quota='download'"+tx.forUpdate(), t.team).Scan(&prior, &used); err != nil {
		return DownloadCharged{}, err
	}
	clockTime2, clockErr2 := tx.now(ctx)
	if clockErr2 != nil {
		return DownloadCharged{}, clockErr2
	}
	now = clockTime2
	day := now.Format("2006-01-02")
	if prior != day {
		used = 0
	}
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(amount),0) FROM quota_reservations WHERE team=? AND quota='download' AND period=? AND state='held' AND expires_at>?", t.team, day, now.UnixNano()).Scan(&reserved); err != nil {
		return DownloadCharged{}, err
	}
	if used < 0 || reserved < 0 || used > math.MaxInt64-reserved || c.Bytes > math.MaxInt64-used-reserved {
		return DownloadCharged{}, fmt.Errorf("download accounting overflow")
	}
	out := DownloadCharged{Tier: TeamTierFunded}
	if holdsFreeAllowance(t.team) {
		funded, err := teamFundedTx(ctx, tx, t.team)
		if err != nil {
			return DownloadCharged{}, err
		}
		out.CapBytes = c.FundedCapBytes
		if !funded {
			out.Tier, out.CapBytes = TeamTierFree, c.FreeCapBytes
		}
	}
	if !c.Record && out.CapBytes > 0 && used+reserved >= out.CapBytes {
		remedy := "the cap resets at midnight UTC"
		if out.Tier == TeamTierFree && c.FundedCapBytes > c.FreeCapBytes {
			remedy = fmt.Sprintf("add credits to the team to raise it to %d bytes a day, or wait for midnight UTC", c.FundedCapBytes)
		}
		return DownloadCharged{}, &DownloadCapError{Team: string(t.team), Day: day, UsedBytes: used + reserved, CapBytes: out.CapBytes, RequestedBytes: c.Bytes, RetryAfter: now.Truncate(24 * time.Hour).Add(24 * time.Hour).Sub(now), Remedy: remedy}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE quota_reservations SET state='released' WHERE team=? AND quota='download' AND state='held' AND expires_at<=?", t.team, now.UnixNano()); err != nil {
		return DownloadCharged{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE quota_admission SET period=?,used=?,reserved=?,updated_at=? WHERE team=? AND quota='download'", day, used+c.Bytes, reserved, now.UnixNano(), t.team); err != nil {
		return DownloadCharged{}, err
	}
	if c.Bytes > 0 {
		result, err := tx.ExecContext(ctx, `INSERT INTO team_download_day(team,period,replica_id,bytes,updated_at) VALUES (?,?,?,?,?)
ON CONFLICT(team,period,replica_id) DO UPDATE SET bytes=team_download_day.bytes+excluded.bytes,updated_at=excluded.updated_at
WHERE team_download_day.bytes<=9223372036854775807-excluded.bytes`, t.team, day, c.ReplicaID, c.Bytes, now.UnixNano())
		if err != nil {
			return DownloadCharged{}, err
		}
		if err := requireOneRow(result, "download usage"); err != nil {
			return DownloadCharged{}, err
		}
	}
	out.DayBytes = used + c.Bytes
	return out, nil
}
