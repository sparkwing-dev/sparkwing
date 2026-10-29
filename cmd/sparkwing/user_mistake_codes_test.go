package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestUserMistakesAreNotReportedAsInternal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("SPARKWING_HOME", home+"/.sparkwing")
	t.Setenv("SPARKWING_CONFIG", home+"/.config/sparkwing/config.yaml")
	empty := t.TempDir()
	t.Chdir(empty)
	for _, args := range [][]string{
		{"pipeline", "describe", "--pipeline", "x"},
		{"run", "x"},
		{"docs", "read", "--topic", "nosuch"},
		{"profiles", "show", "--profile", "nosuch"},
		{"profiles", "remove", "--profile", "nosuch"},
		{"profiles", "set", "--profile", "nosuch", "--controller", "https://ci.example.com"},
		{"examples", "--example", "nosuch"},
	} {
		err := runSparkwing(append(args, "-o", "json"))
		if err == nil {
			t.Errorf("%v succeeded", args)
			continue
		}
		var out bytes.Buffer
		if werr := writeInvocationError(&out, err, apierr.WriteJSON); werr != nil {
			t.Fatal(werr)
		}
		var doc struct {
			Error apierr.Error `json:"error"`
		}
		if jerr := json.Unmarshal(out.Bytes(), &doc); jerr != nil {
			t.Fatalf("%v: %v: %s", args, jerr, &out)
		}
		if args[0] == "profiles" && doc.Error.Code != apierr.NotFound {
			t.Errorf("sparkwing %s reports a missing profile as %q, want not_found", strings.Join(args, " "), doc.Error.Code)
		}
		if doc.Error.Code == apierr.Internal || doc.Error.Code == "" {
			t.Errorf("sparkwing %s reports a user mistake as %q: %s", strings.Join(args, " "), doc.Error.Code, doc.Error.Message)
		}
	}
}
