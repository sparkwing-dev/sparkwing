package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store/storetest"
)

func TestControllerAuthorityIdentityIsDurableAndInternal(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	var generated string
	err := s.DB().QueryRowContext(ctx, `SELECT value FROM sparkwing_meta WHERE key = 'controller_authority_id'`).Scan(&generated)
	if !strings.HasPrefix(generated, "swfa_") || len(generated) != len("swfa_")+32 {
		t.Fatalf("generated authority identity = %q, %v", generated, err)
	}
	var exposed int
	if err := s.DB().QueryRowContext(ctx, storetest.Rebind(s, `SELECT COUNT(*) FROM executors WHERE name = ? OR token_prefix = ?`), generated, generated).Scan(&exposed); err != nil || exposed != 0 {
		t.Fatalf("authority identity entered executor-facing columns: count=%d err=%v", exposed, err)
	}
}
