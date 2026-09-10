package logpretty

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestDefaultRendererHonorsColorPolicy(t *testing.T) {
	old := color.Enabled()
	defer color.SetEnabled(old)
	color.SetEnabled(false)
	t.Setenv("NO_COLOR", "")
	p := NewPrettyRenderer()
	var out bytes.Buffer
	p.w = &out
	p.errW = &out
	p.Emit(sparkwing.LogRecord{JobID: "build", Level: "error", Msg: "failed"})
	p.Flush()
	if strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("color-disabled output contains ANSI: %q", out.String())
	}
}
