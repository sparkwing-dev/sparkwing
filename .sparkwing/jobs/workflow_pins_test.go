package jobs

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var workflowUsesLine = regexp.MustCompile(`(?m)^\s*-?\s*uses:\s*(\S+)\s*(#.*)?$`)

type workflowPin struct {
	action string
	sha    string
	label  string
	file   string
}

func workflowPins(t *testing.T) []workflowPin {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	var pins []workflowPin
	for _, e := range entries {
		if e.IsDir() || (!strings.HasSuffix(e.Name(), ".yaml") && !strings.HasSuffix(e.Name(), ".yml")) {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, m := range workflowUsesLine.FindAllStringSubmatch(string(body), -1) {
			ref := m[1]
			if strings.HasPrefix(ref, "./") {
				continue
			}
			action, sha, _ := strings.Cut(ref, "@")
			pins = append(pins, workflowPin{
				action: action,
				sha:    sha,
				label:  strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(m[2]), "#")),
				file:   e.Name(),
			})
		}
	}
	if len(pins) == 0 {
		t.Fatal("no workflow action references found; the scan matched nothing")
	}
	return pins
}

// A shared action drifting between workflows is how release.yaml kept a
// deprecated runtime while every other workflow moved off it.
func TestWorkflowsAgreeOnOneVersionPerAction(t *testing.T) {
	type usage struct{ files []string }
	byAction := map[string]map[string]*usage{}
	for _, pin := range workflowPins(t) {
		if byAction[pin.action] == nil {
			byAction[pin.action] = map[string]*usage{}
		}
		key := pin.sha + " " + pin.label
		if byAction[pin.action][key] == nil {
			byAction[pin.action][key] = &usage{}
		}
		u := byAction[pin.action][key]
		if len(u.files) == 0 || u.files[len(u.files)-1] != pin.file {
			u.files = append(u.files, pin.file)
		}
	}

	actions := make([]string, 0, len(byAction))
	for action := range byAction {
		actions = append(actions, action)
	}
	sort.Strings(actions)

	for _, action := range actions {
		if len(byAction[action]) == 1 {
			continue
		}
		var lines []string
		for key, u := range byAction[action] {
			lines = append(lines, key+" in "+strings.Join(u.files, ", "))
		}
		sort.Strings(lines)
		t.Errorf("%s is pinned at %d different versions:\n  %s", action, len(byAction[action]), strings.Join(lines, "\n  "))
	}
}
