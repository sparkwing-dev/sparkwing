package clisurface

// safety: an entry only points at the command that took the flag over and never
// runs it, so it is not an alias for the old flag.
var movedFlags = map[string]string{
	"sparkwing runs logs --grep": "search log text with sparkwing runs grep --grep PATTERN",
}
