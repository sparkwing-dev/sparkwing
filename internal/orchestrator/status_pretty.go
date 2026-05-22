package orchestrator

import "github.com/sparkwing-dev/sparkwing/pkg/color"

// colorStatus returns the run-status word with an outcome-tinted
// color. Mirrors the renderer's status palette: success=green,
// failed/cancelled=red, running/pending=cyan, anything else dim.
func colorStatus(status string) string {
	switch status {
	case "success":
		return color.Green(status)
	case "failed", "cancelled":
		return color.Red(status)
	case "running", "pending":
		return color.Cyan(status)
	default:
		return color.Dim(status)
	}
}

// colorOutcome returns a node outcome with the matching tint.
func colorOutcome(outcome string) string {
	switch outcome {
	case "success":
		return color.Green(outcome)
	case "failed":
		return color.Red(outcome)
	case "skipped":
		return color.Dim(outcome)
	case "":
		return "-"
	default:
		return outcome
	}
}

// colorStepGlyph wraps stepGlyph's unicode marker with a
// status-matching color.
func colorStepGlyph(status string) string {
	g := stepGlyph(status)
	switch status {
	case "passed":
		return color.Green(g)
	case "failed":
		return color.Red(g)
	case "skipped":
		return color.Dim(g)
	case "running":
		return color.Cyan(g)
	default:
		return g
	}
}
