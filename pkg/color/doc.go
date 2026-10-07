// Package color provides ANSI color helpers for pipeline output.
//
//	fmt.Println(color.Green("deployed %s", version))
//	fmt.Println(color.Dim("skipping %s", name))
//	fmt.Printf("status: %s %s\n", color.Bold("PASS"), color.Dim(duration))
//
// Color emission auto-detects: enabled only when stdout is a TTY and
// neither NO_COLOR nor CI is set. Agents (Claude Code, Cursor, etc.)
// and pipes get plain text. CLICOLOR_FORCE=1
// re-enables for the rare case the user wants color through a pipe.
//
// The color helpers ([Red], [Green], [Yellow], [Blue], [Magenta],
// [Cyan], [Bold], [Dim]) all share the same signature: variadic args
// in `fmt.Sprint`-style. [Enabled] / [SetEnabled] inspect or force
// the detection result; [IsInteractiveStdout] is the underlying TTY
// check used by other code paths that need the same definition of
// "interactive".
package color
