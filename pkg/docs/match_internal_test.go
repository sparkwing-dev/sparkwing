package docs

import "testing"

// Substring matching on headings made the generated CLI reference's
// subcommand tables the top hit for "run shell command": "command" is
// buried inside "Subcommands", which scored as a heading match, and the
// table won the shorter-is-better tie-break over the page that explains
// how to run a shell command. A word has to match as a word.
func TestHeadingSubstringsDoNotOutrankRealMatches(t *testing.T) {
	if wholeWord("subcommands", "command") {
		t.Error(`"command" matched "subcommands" as a whole word`)
	}
	if wordPrefix("subcommands", "command") {
		t.Error(`"command" matched "subcommands" as a word prefix`)
	}
	if !wordPrefix("exec - shelling out", "shell") {
		t.Error(`"shell" did not match "shelling" as a word prefix`)
	}
	// A plural is a prefix match, not a whole-word one -- which is
	// exactly how the scaffolder's "on: trigger" tip reaches the
	// section headed "Triggers".
	if wholeWord("triggers (`on:`)", "trigger") {
		t.Error(`"trigger" matched "triggers" as a whole word`)
	}
	if !wordPrefix("triggers (`on:`)", "trigger") {
		t.Error(`"trigger" did not match "triggers" as a word prefix`)
	}
}
