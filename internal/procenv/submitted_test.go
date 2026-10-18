package procenv

import (
	"encoding/hex"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func FuzzSubmittedEnvironmentFiltersAndMasks(f *testing.F) {
	f.Add([]byte("private input"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 256 {
			return
		}
		value := "submitted-value-" + hex.EncodeToString(raw)
		masker := secrets.NewMasker()
		got, err := Submit([]string{"APP_NOTE=" + value, "APP_EMPTY=", "UNLISTED=" + value, "APP_TOKEN=" + value, "SPARKWING_RUN_ID=" + value}, []string{"APP_*", "MISSING", "SPARKWING_*"}, masker)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || string(got["APP_NOTE"]) != value || got["APP_EMPTY"] == nil {
			t.Fatal("submitted values escaped their allow-list or lost presence")
		}
		if masker.Mask(value) == value {
			t.Fatal("submitted value was not masked")
		}
	})
}

func TestSubmittedEnvironmentPreservesNativeBytes(t *testing.T) {
	value := string([]byte{255, 254, 'x'})
	got, err := Submit([]string{"EXACT=" + value}, []string{"EXACT"}, secrets.NewMasker())
	if err != nil || string(got["EXACT"]) != value {
		t.Fatal("submitted native bytes changed", err)
	}
	if _, err := Submit(nil, []string{"*"}, secrets.NewMasker()); err == nil {
		t.Fatal("invalid allow-list accepted")
	}
}

func TestSubmittedEnvironmentRejectsDuplicateProcessNames(t *testing.T) {
	if _, err := Submit([]string{"BUILD_NOTE=first", "BUILD_NOTE=second"}, []string{"BUILD_*"}, secrets.NewMasker()); err == nil {
		t.Fatal("duplicate submitted assignments accepted")
	}
	if runtime.GOOS == "windows" {
		if _, err := WithSubmitted(Base{Masker: secrets.NewMasker()}, map[string][]byte{"BUILD_NOTE": []byte("first"), "build_note": []byte("second")}); err == nil {
			t.Fatal("case-insensitive duplicate submitted assignments accepted")
		}
	}
}
