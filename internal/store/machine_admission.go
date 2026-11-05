package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// LoadMachineAdmission reads the controller's complete machine-admission state.
// A store without a saved state returns nil and no error.
func (s *Store) LoadMachineAdmission(ctx context.Context) ([]byte, error) {
	var state []byte
	err := s.queryRow(ctx, `SELECT state_json FROM local_machine_admission WHERE singleton=1`).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load machine admission: %w", err)
	}
	return state, nil
}

// SaveMachineAdmission atomically replaces the controller's complete JSON state,
// including the ledger and request receipts that identify retries.
func (s *Store) SaveMachineAdmission(ctx context.Context, state []byte) error {
	if !json.Valid(state) {
		return errors.New("save machine admission: invalid JSON state")
	}
	err := s.inTx(ctx, func(tx *storeTx) error {
		_, err := tx.execContext(ctx, `INSERT INTO local_machine_admission(singleton,state_json) VALUES (1,?)
ON CONFLICT(singleton) DO UPDATE SET state_json=excluded.state_json`, state)
		return err
	})
	if err != nil {
		return fmt.Errorf("save machine admission: %w", err)
	}
	return nil
}
