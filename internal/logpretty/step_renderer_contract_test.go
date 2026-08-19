package logpretty

import (
	"io"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var (
	_ func(*PrettyRenderer, io.Writer, sparkwing.LogRecord) = (*PrettyRenderer).writeStepStart
	_ func(*PrettyRenderer, io.Writer, sparkwing.LogRecord) = (*PrettyRenderer).writeStepEnd
	_ func(*PrettyRenderer, io.Writer, sparkwing.LogRecord) = (*PrettyRenderer).writeStepSkipped
)
