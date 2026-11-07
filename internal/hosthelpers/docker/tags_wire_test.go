package docker

import (
	"encoding/json"
	sdkdocker "github.com/sparkwing-dev/sparkwing/sparkwing/docker"
	"testing"
)

func TestImageTagWireFieldsReachBothSDKs(t *testing.T) {
	raw, err := json.Marshal(ImageTag{Commit: "abcdef", Content: "123456", Branch: "main", Dirty: true})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["commit"] != "abcdef" || fields["content"] != "123456" || fields["branch"] != "main" || fields["dirty"] != true || len(fields) != 4 {
		t.Fatalf("TypeScript wire fields: %s", raw)
	}
	var value sdkdocker.ImageTag
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if value.DeployTag() != "commit-abcdef-files-123456-dirty" {
		t.Fatalf("Go value: %+v", value)
	}
}
