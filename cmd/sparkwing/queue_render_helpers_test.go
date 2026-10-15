package main

import (
	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"io"
)

func renderNoDaemon(w io.Writer, format string) error {
	return opsview.RenderNoDaemon(w, format)
}

func renderQueuePretty(w io.Writer, qs wingwire.QueueState) error {
	return opsview.RenderQueuePretty(w, qs)
}

func containerNote(c *wingwire.ContainerLimit) string { return opsview.ContainerNote(c) }

func budgetNote(b *wingwire.BudgetState) string { return opsview.BudgetNote(b) }

func fmtEventsLine(ev *wingwire.EventsWindow) string { return opsview.FmtEventsLine(ev) }

func fmtDaemonHeader(qs wingwire.QueueState) string { return opsview.FmtDaemonHeader(qs) }

func originWord(o wingwire.Origin) string { return opsview.OriginWord(o) }

func externalPressureNote(qs wingwire.QueueState) string { return opsview.ExternalPressureNote(qs) }
