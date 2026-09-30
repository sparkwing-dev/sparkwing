package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestSchemaV49RestoresMissingNodeClaimTokenPrefix(t *testing.T) {
	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	readyNode(t, st, "upgrade-run", "local-node")
	for _, statement := range []string{
		`UPDATE nodes SET started_at = 1000000000, finished_at = 4000000000`,
		`ALTER TABLE nodes DROP COLUMN claim_token_prefix`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 49`,
	} {
		if _, err := st.DB().ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up := target.Open(t)
	settlement, err := up.NodeSettlement(ctx, "upgrade-run", "local-node")
	if err != nil {
		t.Fatalf("read settlement after upgrading: %v", err)
	}
	if settlement.Seconds != 3 || settlement.ClaimTokenPrefix != "" || settlement.ChargeWindowOpen {
		t.Fatalf("settlement = %+v, want three seconds without a claim or charge window", settlement)
	}
}
