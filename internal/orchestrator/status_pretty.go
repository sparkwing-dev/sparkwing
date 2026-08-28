package orchestrator

import "github.com/sparkwing-dev/sparkwing/pkg/color"

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

func colorOutcome(outcome string) string {
	switch outcome {
	case "success":
		return color.Green(outcome)
	case "failed", "cancelled":
		return color.Red(outcome)
	case "skipped":
		return color.Dim(outcome)
	case "":
		return "-"
	default:
		return outcome
	}
}

func colorStepGlyph(status string) string {
	g := stepGlyph(status)
	switch status {
	case "passed":
		return color.Green(g)
	case "failed", "cancelled":
		return color.Red(g)
	case "skipped":
		return color.Dim(g)
	case "running":
		return color.Cyan(g)
	default:
		return g
	}
}
