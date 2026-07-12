package main

import "testing"

func TestBanned_FlagsPhantomPipelineAddVerb(t *testing.T) {
	const phantom = "register the repo with `sparkwing pipeline add <path>`"
	for _, b := range banned {
		if b.re.MatchString(phantom) {
			return
		}
	}
	t.Fatalf("no banned pattern flags the phantom `sparkwing pipeline add` verb")
}
