package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

func usageStatement(counter string) (string, error) {
	var table, column string
	switch counter {
	case "egress_bytes":
		table, column = "egress_usage", "bytes"
	case "download_bytes":
		table, column = "team_download_day", "bytes"
	case "storage_bytes":
		table, column = "storage_month_usage", "bytes"
	case "storage_objects":
		table, column = "storage_month_usage", "objects"
	case "runs":
		return `INSERT INTO run_rate_usage(team,period,principal,runs,updated_at) VALUES (?,?,'',?,?)
ON CONFLICT(team,period,principal) DO UPDATE SET runs = run_rate_usage.runs + excluded.runs, updated_at = excluded.updated_at
WHERE run_rate_usage.runs <= 9223372036854775807 - excluded.runs`, nil
	default:
		return "", fmt.Errorf("unknown usage counter %q", counter)
	}
	return `INSERT INTO ` + table + `(team,period,` + column + `,updated_at) VALUES (?,?,?,?)
ON CONFLICT(team,period) DO UPDATE SET ` + column + ` = ` + table + `.` + column + ` + excluded.` + column + `, updated_at = excluded.updated_at
WHERE ` + table + `.` + column + ` <= 9223372036854775807 - excluded.` + column, nil
}

func numericSettingTx(ctx context.Context, tx *storeTx, key string, fallback int64) (int64, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	if err != nil {
		return 0, err
	}
	var value *int64
	if err := json.Unmarshal(raw, &value); err != nil || value == nil || *value < 0 {
		return 0, fmt.Errorf("setting %s requires a nonnegative integer", key)
	}
	return *value, nil
}
