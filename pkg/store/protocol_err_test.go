package store

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsProtocolErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"locking protocol text", errors.New("locking protocol (15)"), true},
		{"sqlite_protocol code", errors.New("SQLITE_PROTOCOL"), true},
		{"wrapped", fmt.Errorf("resolve waiter: %w", errors.New("locking protocol (15)")), true},
		{"busy is not protocol", errors.New("database is locked (5) (SQLITE_BUSY)"), false},
		{"unrelated", errors.New("no such table: runs"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsProtocolErr(tc.err); got != tc.want {
				t.Errorf("IsProtocolErr(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}
