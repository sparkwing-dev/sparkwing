package controller

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestCloudCapacityNoticeUsesStoreElapsedTime(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	view := store.RunView{Snapshot: store.RunSnapshot{StoreNow: since.Add(95 * time.Second)}, CloudCapacitySince: &since}
	e := &EngineController{}
	facts, err := e.runViewStatus(t.Context(), view)
	if err != nil || len(facts.Notices) != 1 || facts.Notices[0].Kind != engineid.NoticeCloudCapacity || facts.Notices[0].Reason != "Waiting for dedicated Cloud capacity (elapsed 1m35s)" || !strings.Contains(facts.Notices[0].Hint, "credit hold") {
		t.Fatalf("capacity notice: %+v %v", facts.Notices, err)
	}
	view.CloudCapacitySince = nil
	facts, err = e.runViewStatus(t.Context(), view)
	if err != nil || len(facts.Notices) != 0 {
		t.Fatalf("cleared notice: %+v %v", facts.Notices, err)
	}
}
