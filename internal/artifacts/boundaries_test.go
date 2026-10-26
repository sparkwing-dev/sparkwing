package artifacts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

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
