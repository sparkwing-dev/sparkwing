//go:build darwin

package wingd

import "testing"

func TestDarwinFreeMemory_UnreadableLevelReportsNoMeasurement(t *testing.T) {
	const total = 17179869184

	cases := []struct {
		name     string
		level    uint32
		read     bool
		wantFree uint64
		wantOK   bool
	}{
		{"sysctl failed", 0, false, 0, false},
		{"level zero", 0, true, 0, false},
		{"level out of range", 101, true, 0, false},
		{"idle machine", 37, true, 6356551598, true},
		{"under pressure", 20, true, 3435973836, true},
		{"nothing free but still serving", 1, true, 171798691, true},
		{"everything free", 100, true, total, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			free, ok := darwinFreeMemory(total, tc.level, tc.read)
			if ok != tc.wantOK {
				t.Fatalf("measured = %v, want %v", ok, tc.wantOK)
			}
			if free != tc.wantFree {
				t.Fatalf("free = %d, want %d", free, tc.wantFree)
			}
		})
	}
}

func TestSampleHost_NeverClaimsAMeasurementItDoesNotHave(t *testing.T) {
	stat, err := sampleHost()
	if err != nil {
		t.Fatalf("sample host: %v", err)
	}
	if stat.MemoryMeasured && stat.FreeMemoryBytes == 0 {
		t.Fatal("memory reported measured with zero bytes free; a measurement that cannot be told from a full machine is not one")
	}
	if !stat.MemoryMeasured && stat.FreeMemoryBytes != 0 {
		t.Fatalf("unmeasured memory carries %d bytes; an unread dimension must carry no figure", stat.FreeMemoryBytes)
	}
	if stat.TotalMemoryBytes == 0 {
		t.Fatal("host total memory is zero")
	}
}
