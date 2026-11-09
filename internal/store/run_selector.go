package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
)

// ResolveRunID resolves a stable ID or canonical positive team number without reading run contents.
func (t *Tenant) ResolveRunID(ctx context.Context, selector string) (string, error) {
	column, value := "id", any(selector)
	if number, err := strconv.ParseInt(selector, 10, 64); err == nil && number > 0 && strconv.FormatInt(number, 10) == selector {
		column, value = "number", number
	}
	var id string
	err := t.s.queryRow(ctx, "SELECT id FROM runs WHERE team=? AND "+column+"=?", t.team, value).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", notFound("run", selector)
	}
	return id, err
}
