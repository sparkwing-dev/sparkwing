package artifacts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestStageBudgetAndCancellationPreserveDestination(t *testing.T) {
	for _, fault := range []string{"no_budget", "expired", "cancelled", "lookup", "manifest_close", "member_close"} {
		t.Run(fault, func(t *testing.T) {
			data, bytes, records := testData()
			producer := t.TempDir()
			if err := os.WriteFile(filepath.Join(producer, "out"), []byte("new"), 0o644); err != nil {
				t.Fatal(err)
			}
			record, err := Capture(t.Context(), data, producer, []string{"out"}, func(string) {})
			if err != nil {
				t.Fatal(err)
			}
			records.result = blob.RecordLookupResult{Found: true, Record: record, Members: records.commits[0].Members}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := context.Canceled
			switch fault {
			case "no_budget":
				data.Budget.Deadline = time.Time{}
				want = blob.ErrBudget
			case "expired":
				data.Budget.Deadline = time.Unix(1, 0)
				want = blob.ErrBudget
			case "cancelled":
				cancel()
			case "lookup":
				records.onLookup = func(context.Context) { cancel() }
			default:
				target := record.Digest
				if fault == "member_close" {
					target = records.result.Members[0].Digest
				}
				bytes.onClose = func(digest blob.Digest) {
					if digest == target {
						cancel()
					}
				}
			}
			consumer := t.TempDir()
			if err := os.WriteFile(filepath.Join(consumer, "out"), []byte("preserved"), 0o444); err != nil {
				t.Fatal(err)
			}
			if n, err := Stage(ctx, data, consumer, []Edge{{Record: record.Key}}); n != 0 || !errors.Is(err, want) {
				t.Fatalf("stage = %d, %v; want %v", n, err, want)
			}
			if body, err := os.ReadFile(filepath.Join(consumer, "out")); err != nil || string(body) != "preserved" {
				t.Fatalf("destination = %q, %v", body, err)
			}
			if entries, err := os.ReadDir(consumer); err != nil || len(entries) != 1 {
				t.Fatalf("staging cleanup = %v, %v", entries, err)
			}
		})
	}
}

func TestStageCarriesBudgetThroughTransfers(t *testing.T) {
	data, bytes, records := testData()
	producer := t.TempDir()
	if err := os.WriteFile(filepath.Join(producer, "out"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	record, err := Capture(t.Context(), data, producer, []string{"out"}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	records.result = blob.RecordLookupResult{Found: true, Record: record, Members: records.commits[0].Members}
	check := func(ctx context.Context) {
		t.Helper()
		if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(data.Budget.Deadline) {
			t.Errorf("operation deadline = %v, %v; want %v", deadline, ok, data.Budget.Deadline)
		}
	}
	records.onLookup = check
	bytes.onGet = func(ctx context.Context, _ blob.GetRequest) { check(ctx) }
	if n, err := Stage(t.Context(), data, t.TempDir(), []Edge{{Record: record.Key}}); err != nil || n != 1 {
		t.Fatalf("stage = %d, %v", n, err)
	}
}

func TestCaptureRefusesMismatchedUploadAndRecord(t *testing.T) {
	for _, fault := range []string{"file_receipt", "manifest_receipt", "record_key", "record_digest", "record_source", "uploader", "cancel_after_upload"} {
		t.Run(fault, func(t *testing.T) {
			data, bytes, records := testData()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := blob.ErrIntegrity
			switch fault {
			case "file_receipt", "manifest_receipt":
				bytes.receipt = func(receipt blob.Receipt) blob.Receipt {
					if fault == "file_receipt" || len(bytes.puts) == 2 {
						receipt.Identity.Attempt.Value.Generation++
					}
					return receipt
				}
			case "record_key", "record_digest", "record_source":
				records.record = func(record blob.Record) blob.Record {
					switch fault {
					case "record_key":
						record.Key.Scope.Job = "other"
					case "record_digest":
						record.Digest = digestOf([]byte("other"))
					case "record_source":
						record.Source.Attempt.Value.Generation++
					}
					return record
				}
			case "uploader":
				data.Identity.Attempt.Value.Generation++
				want = blob.ErrDigestNotReadable
			case "cancel_after_upload":
				bytes.onPut = cancel
				want = context.Canceled
			}
			producer := t.TempDir()
			if err := os.WriteFile(filepath.Join(producer, "out"), []byte("new"), 0o644); err != nil {
				t.Fatal(err)
			}
			if record, err := Capture(ctx, data, producer, []string{"out"}, func(string) {}); record.Digest != "" || !errors.Is(err, want) {
				t.Fatalf("capture = %+v, %v; want %v", record, err, want)
			}
			if (fault == "uploader" || fault == "file_receipt" || fault == "manifest_receipt" || fault == "cancel_after_upload") && len(records.commits) != 0 {
				t.Fatal("invalid upload reached record commit")
			}
		})
	}
}
