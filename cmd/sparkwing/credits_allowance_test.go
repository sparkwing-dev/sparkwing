package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRenderCreditStateSeparatesStorageFromRunnerTime(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := renderCreditState(&buf, creditStateResp{
		BalanceMicro:              900_000_000,
		GrantedMicro:              1_000_000_000,
		ChargedMicro:              100_000_000,
		StorageChargedMicro:       666_666,
		StorageRateMicroPerGBDay:  store.CloudStorageRateMicroPerGBDay,
		StorageFreeAllowanceBytes: 1 << 30,
		MicroPerCredit:            store.MicroCreditsPerCredit,
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"STORAGE CHARGED", "133.33 credits", "STORAGE RATE", "66.666600", "1073741824"} {
		if !strings.Contains(out, want) {
			t.Errorf("credit state output is missing %q:\n%s", want, out)
		}
	}
}

func TestRenderCreditStateSaysWhenStorageIsNotBilled(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := renderCreditState(&buf, creditStateResp{
		MicroPerCredit: store.MicroCreditsPerCredit,
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), "retained bytes are not billed") {
		t.Errorf("an unbilled install reads:\n%s", buf.String())
	}
}

func TestCreditSettingsBodyCarriesTheStorageFlags(t *testing.T) {
	t.Parallel()
	fs := creditSettingsFlagSet(t, []string{
		"--storage-rate-micro-per-gb-day", "333333",
		"--storage-free-allowance-bytes", "1073741824",
	})
	body, err := creditSettingsBody(fs, creditSettingsFlags{
		storageRate: store.CloudStorageRateMicroPerGBDay, storageFree: 1 << 30,
	})
	if err != nil {
		t.Fatalf("body: %v", err)
	}
	if len(body) != 2 || body["storage_rate_micro_per_gb_day"] != int64(333_333) ||
		body["storage_free_allowance_bytes"] != int64(1<<30) {
		t.Fatalf("body = %v, want the two storage settings alone", body)
	}
}

func TestCreditHistoryRowNamesTheTeamAndBytesAStorageChargeBilled(t *testing.T) {
	t.Parallel()
	rows := creditHistoryRows(creditHistoryResp{
		Charges: []creditChargeResp{{
			ID: "charge-s", Kind: store.CreditChargeStorage, AmountMicro: 1_666_666,
			Principal: "acme", StorageBytes: 2 << 30, Seconds: 86_400, ChargedAt: 100,
		}},
	})
	if len(rows) != 1 || rows[0].Type != store.CreditChargeStorage {
		t.Fatalf("rows = %+v, want one storage row", rows)
	}
	detail := creditRowDetail(rows[0])
	for _, want := range []string{"team=acme", "2147483648 bytes", "86400s"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q is missing %q", detail, want)
		}
	}
}

func TestAllowanceReadsTheCallersOwnTeamOrAnotherTheTokenHolds(t *testing.T) {
	t.Parallel()
	state := storageStateResp{
		Quota:  storageQuotaResp{Principal: "root", StorageAllowanceBytes: 1 << 30},
		Quotas: []storageQuotaResp{{Principal: "acme", StorageAllowanceBytes: 50 << 30}},
	}
	state.Usage.RetainedBytes = 123

	own, retained, err := allowanceForPrincipal(state, "")
	if err != nil || own.Principal != "root" || retained != 123 {
		t.Fatalf("own = %+v retained=%d err=%v, want the calling token's team", own, retained, err)
	}
	other, _, err := allowanceForPrincipal(state, "acme")
	if err != nil || other.StorageAllowanceBytes != 50<<30 {
		t.Fatalf("other = %+v err=%v, want the team out of the admin list", other, err)
	}
	if _, _, err := allowanceForPrincipal(state, "ghost"); err == nil {
		t.Fatal("a team this token holds no quota for was answered")
	}
}

func TestAllowanceLineSaysWhenNoCeilingWasNamed(t *testing.T) {
	t.Parallel()
	if !strings.Contains(allowanceLine(0), "every retained byte is kept") {
		t.Errorf("a zero allowance reads %q", allowanceLine(0))
	}
	if !strings.Contains(allowanceLine(1<<30), "1073741824 bytes") {
		t.Errorf("a set allowance reads %q", allowanceLine(1<<30))
	}
}

func TestAllowanceRefusesGibibytesAndBytesTogether(t *testing.T) {
	t.Parallel()
	err := runCreditsAllowance([]string{"--profile", "none", "--gb", "1", "--bytes", "2"})
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("naming both units = %v, want a refusal", err)
	}
}
