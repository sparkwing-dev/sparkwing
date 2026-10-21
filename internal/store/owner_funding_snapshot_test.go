package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestOwnerSnapshotUsesClaimFundingWithoutAwarding(t *testing.T) {
	storetest.New(t).Open(t).TestOnlyOwnerSnapshotFunding(t)
}
