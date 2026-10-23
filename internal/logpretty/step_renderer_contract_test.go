package logpretty

import (
	"io"
)

var (
	_ func(*PrettyRenderer, io.Writer, Record) = (*PrettyRenderer).writeStepStart
	_ func(*PrettyRenderer, io.Writer, Record) = (*PrettyRenderer).writeStepEnd
	_ func(*PrettyRenderer, io.Writer, Record) = (*PrettyRenderer).writeStepSkipped
)
