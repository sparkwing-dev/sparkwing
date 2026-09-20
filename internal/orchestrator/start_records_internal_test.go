package orchestrator

import (
	"path/filepath"
	"testing"
)

func TestWithDiskAttrs_UnreadableVolumeOmitsTheAttrs(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nothing-created-this")
	for _, root := range []string{"", missing} {
		if got := withDiskAttrs(nil, root); got != nil {
			t.Errorf("withDiskAttrs(nil, %q) = %v; want nil", root, got)
		}
		got := withDiskAttrs(map[string]any{"pipeline": "widget-build"}, root)
		if _, ok := got["disk_free_bytes"]; ok {
			t.Errorf("withDiskAttrs(_, %q) = %v; want no disk attrs", root, got)
		}
	}
}

func TestWithDiskAttrs_LeavesTheCallersMapAlone(t *testing.T) {
	base := map[string]any{"pipeline": "widget-build"}
	out := withDiskAttrs(base, t.TempDir())
	if _, ok := base["disk_free_bytes"]; ok {
		t.Error("withDiskAttrs wrote into the caller's map, so a record taken before the call now carries disk attrs")
	}
	if out["pipeline"] != "widget-build" {
		t.Errorf("withDiskAttrs = %v; want the caller's entries kept", out)
	}
	if _, ok := out["disk_free_bytes"]; !ok {
		t.Errorf("withDiskAttrs = %v; want a reading for a readable volume", out)
	}
}
