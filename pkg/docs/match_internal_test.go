package docs

import "testing"

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

	if wholeWord("triggers (`on:`)", "trigger") {
		t.Error(`"trigger" matched "triggers" as a whole word`)
	}
	if !wordPrefix("triggers (`on:`)", "trigger") {
		t.Error(`"trigger" did not match "triggers" as a word prefix`)
	}
}
