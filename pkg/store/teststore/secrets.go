package teststore

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// SeedSecret installs secret metadata for read and transport fixtures.
func SeedSecret(s *store.Store, team store.Team, row store.Secret, at time.Time) error {
	masked, shared := 0, 0
	if row.Masked {
		masked = 1
	}
	if row.Shared {
		shared = 1
	}
	_, err := Exec(context.Background(), s, `INSERT INTO secrets(team,name,value,principal,pipeline,masked,shared,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(team,name,pipeline) DO UPDATE SET value=excluded.value,principal=excluded.principal,masked=excluded.masked,shared=excluded.shared,updated_at=excluded.updated_at`,
		team, row.Name, row.Value, row.Principal, row.Pipeline, masked, shared, at.UnixNano(), at.UnixNano())
	return err
}
