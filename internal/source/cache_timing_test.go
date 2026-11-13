package source_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestPreparationReportsCacheOutcomeAndTransferredBytes(t *testing.T) {
	f := newPreparedFixture(t, engineid.GoSDK)
	install := f.options.InstallSDK
	f.options.InstallSDK = func(ctx context.Context, selected engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
		command, err := install(ctx, selected, request)
		if err != nil {
			return command, err
		}
		body, err := json.Marshal(command)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		fields["cache"] = json.RawMessage(`{"outcome":"compile","bytes_fetched":0}`)
		body, err = json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &command); err != nil {
			t.Fatal(err)
		}
		return command, nil
	}
	var receipts []source.Timing
	f.options.Timing = func(_ context.Context, receipt source.Timing) { receipts = append(receipts, receipt) }
	f.publish(t)
	entry, err := source.Acquire(t.Context(), f.spec, f.options)
	if err != nil {
		t.Fatal(err)
	}
	if err := entry.Close(); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"sdk_install", "restore"} {
		var found bool
		for _, receipt := range receipts {
			if receipt.Stage != stage || stage == "restore" && !receipt.Reused {
				continue
			}
			found = true
			body, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Cache *engine.SDKCache `json:"cache"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			want := "compile"
			if stage == "restore" {
				want = "hit"
			}
			if got.Cache == nil || got.Cache.Outcome != want || got.Cache.BytesFetched == nil {
				t.Fatalf("missing %s cache evidence: %s", stage, body)
			}
			if stage == "restore" && *got.Cache.BytesFetched != int64(len(f.storage.body)) {
				t.Fatalf("restore bytes=%d, want %d", *got.Cache.BytesFetched, len(f.storage.body))
			}
		}
		if !found {
			t.Fatalf("missing %s receipt", stage)
		}
	}
}

func TestSDKReadyRequiresAValidatedCommand(t *testing.T) {
	f := newPreparedFixture(t, engineid.GoSDK)
	install := f.options.InstallSDK
	f.options.InstallSDK = func(ctx context.Context, selected engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
		command, err := install(ctx, selected, request)
		command.Directory = ""
		return command, err
	}
	var observed *source.Timing
	f.options.Timing = func(_ context.Context, receipt source.Timing) {
		if receipt.Stage == "sdk_install" {
			observed = &receipt
		}
	}
	if _, err := source.Acquire(t.Context(), f.spec, f.options); err == nil {
		t.Fatal("invalid command accepted")
	}
	if observed == nil || observed.Success {
		t.Fatalf("invalid command reported SDK ready: %+v", observed)
	}
}
