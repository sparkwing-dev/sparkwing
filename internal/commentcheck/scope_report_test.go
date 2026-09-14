package main

import (
	"strings"
	"testing"
)

func TestCleanLineSeparatesAPassFromAnUnreadRun(t *testing.T) {
	judgedNothing := cleanLine(0)
	if !strings.Contains(judgedNothing, "judged nothing") {
		t.Errorf("a scoped run over no Go file reports %q, which reads as a pass it never reached", judgedNothing)
	}
	judgedSome := cleanLine(3)
	if !strings.Contains(judgedSome, "3") {
		t.Errorf("a scoped run over three files reports %q, which does not say what it read", judgedSome)
	}
	if judgedSome == judgedNothing {
		t.Error("a run that read nothing and a run that read three files report the same line")
	}
	whole := cleanLine(-1)
	if strings.Contains(whole, "judged nothing") {
		t.Errorf("an unscoped run over the whole tree reports %q", whole)
	}
}
