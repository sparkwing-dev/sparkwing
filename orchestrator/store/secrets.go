package store

import (
	"database/sql"
	"errors"
	"time"
)

// Secret is one row in the secrets table. Masked controls log
// redaction; defaults to true.
type Secret struct {
	Name      string
	Value     string
	Principal string
	Masked    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateOrReplaceSecret upserts a row; created_at is preserved.
func (s *Store) CreateOrReplaceSecret(name, value, principal string, masked bool, now time.Time) error {
	if name == "" {
		return errors.New("secrets: name required")
	}
	ts := now.UTC().Unix()
	maskedInt := 0
	if masked {
		maskedInt = 1
	}
	_, err := s.db.Exec(`
        INSERT INTO secrets (name, value, principal, masked, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT(name) DO UPDATE SET
            value = excluded.value,
            principal = excluded.principal,
            masked = excluded.masked,
            updated_at = excluded.updated_at
    `, name, value, principal, maskedInt, ts, ts)
	return err
}

// GetSecret returns the row including Value.
func (s *Store) GetSecret(name string) (*Secret, error) {
	if name == "" {
		return nil, errors.New("secrets: name required")
	}
	row := s.db.QueryRow(`
        SELECT name, value, principal, masked, created_at, updated_at
          FROM secrets
         WHERE name = ?
    `, name)
	var sec Secret
	var maskedInt int
	var created, updated int64
	if err := row.Scan(&sec.Name, &sec.Value, &sec.Principal, &maskedInt, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	sec.Masked = maskedInt != 0
	sec.CreatedAt = time.Unix(created, 0).UTC()
	sec.UpdatedAt = time.Unix(updated, 0).UTC()
	return &sec, nil
}

// ListSecrets returns rows ordered by name. HTTP handlers must blank
// Value before serializing.
func (s *Store) ListSecrets() ([]Secret, error) {
	rows, err := s.db.Query(`
        SELECT name, value, principal, masked, created_at, updated_at
          FROM secrets
         ORDER BY name
    `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Secret
	for rows.Next() {
		var sec Secret
		var maskedInt int
		var created, updated int64
		if err := rows.Scan(&sec.Name, &sec.Value, &sec.Principal, &maskedInt, &created, &updated); err != nil {
			return nil, err
		}
		sec.Masked = maskedInt != 0
		sec.CreatedAt = time.Unix(created, 0).UTC()
		sec.UpdatedAt = time.Unix(updated, 0).UTC()
		out = append(out, sec)
	}
	return out, nil
}

// DeleteSecret removes the row; ErrNotFound when missing.
func (s *Store) DeleteSecret(name string) error {
	res, err := s.db.Exec(`DELETE FROM secrets WHERE name = ?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
